package activity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/api/historyservice/v1"
	"go.temporal.io/server/chasm"
	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTransitionScheduled_StartDelay(t *testing.T) {
	const (
		startDelay             = 5 * time.Second
		scheduleToStartTimeout = 2 * time.Minute
		scheduleToCloseTimeout = 10 * time.Minute
		heartbeatTimeout       = 30 * time.Second
	)

	ctx := &chasm.MockMutableContext{
		MockContext: chasm.MockContext{
			HandleNow: func(chasm.Component) time.Time { return defaultTime },
		},
	}
	attemptState := &activitypb.ActivityAttemptState{}
	activity := &Activity{
		ActivityState: &activitypb.ActivityState{
			ActivityType:           &commonpb.ActivityType{Name: "test-activity-type"},
			RetryPolicy:            defaultRetryPolicy,
			ScheduleToCloseTimeout: durationpb.New(scheduleToCloseTimeout),
			ScheduleToStartTimeout: durationpb.New(scheduleToStartTimeout),
			StartToCloseTimeout:    durationpb.New(defaultStartToCloseTimeout),
			HeartbeatTimeout:       durationpb.New(heartbeatTimeout),
			StartDelay:             durationpb.New(startDelay),
			ScheduleTime:           timestamppb.New(defaultTime),
			TaskQueue:              &taskqueuepb.TaskQueue{Name: "test-task-queue"},
		},
		LastAttempt: chasm.NewDataField(ctx, attemptState),
		Outcome:     chasm.NewDataField(ctx, &activitypb.ActivityOutcome{}),
		RequestData: chasm.NewDataField(ctx, &activitypb.ActivityRequestData{}),
	}

	require.NoError(t, TransitionScheduled.Apply(activity, ctx, nil))
	require.Equal(t, activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED, activity.Status)

	dispatchTime := defaultTime.Add(startDelay)
	require.Equal(t, dispatchTime, activity.firstDispatchTime())
	require.Equal(t, dispatchTime.Add(scheduleToCloseTimeout), activity.scheduleToCloseDeadline())

	require.Len(t, ctx.Tasks, 3)
	for _, task := range ctx.Tasks {
		switch payload := task.Payload.(type) {
		case *activitypb.ActivityDispatchTask:
			require.Equal(t, dispatchTime, task.Attributes.ScheduledTime)
			require.Equal(t, attemptState.GetStamp(), payload.GetStamp())
		case *activitypb.ScheduleToStartTimeoutTask:
			require.Equal(t, dispatchTime.Add(scheduleToStartTimeout), task.Attributes.ScheduledTime)
		case *activitypb.ScheduleToCloseTimeoutTask:
			require.Equal(t, dispatchTime.Add(scheduleToCloseTimeout), task.Attributes.ScheduledTime)
		default:
			t.Fatalf("unexpected task %T; start-to-close and heartbeat timers must wait for worker pickup", task.Payload)
		}
	}
}

func TestTransitionStarted_StartDelayDoesNotAffectAttemptTimeouts(t *testing.T) {
	const (
		startDelay       = 5 * time.Second
		heartbeatTimeout = 15 * time.Second
	)
	startedAt := defaultTime.Add(startDelay)

	ctx := &chasm.MockMutableContext{
		MockContext: chasm.MockContext{
			HandleNow: func(chasm.Component) time.Time { return startedAt },
		},
	}
	attemptState := &activitypb.ActivityAttemptState{Count: 1}
	activity := &Activity{
		ActivityState: &activitypb.ActivityState{
			RetryPolicy:         defaultRetryPolicy,
			StartToCloseTimeout: durationpb.New(defaultStartToCloseTimeout),
			HeartbeatTimeout:    durationpb.New(heartbeatTimeout),
			StartDelay:          durationpb.New(startDelay),
			ScheduleTime:        timestamppb.New(defaultTime),
			Status:              activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED,
		},
		LastAttempt: chasm.NewDataField(ctx, attemptState),
		Outcome:     chasm.NewDataField(ctx, &activitypb.ActivityOutcome{}),
	}

	err := TransitionStarted.Apply(activity, ctx, &historyservice.RecordActivityTaskStartedRequest{
		PollRequest: &workflowservice.PollActivityTaskQueueRequest{Identity: "test-worker"},
	})
	require.NoError(t, err)

	require.Len(t, ctx.Tasks, 2)
	for _, task := range ctx.Tasks {
		switch task.Payload.(type) {
		case *activitypb.StartToCloseTimeoutTask:
			require.Equal(t, startedAt.Add(defaultStartToCloseTimeout), task.Attributes.ScheduledTime)
		case *activitypb.HeartbeatTimeoutTask:
			require.Equal(t, startedAt.Add(heartbeatTimeout), task.Attributes.ScheduledTime)
		default:
			t.Fatalf("unexpected task %T", task.Payload)
		}
	}
}

func TestTransitionRescheduled_DoesNotReapplyStartDelay(t *testing.T) {
	const (
		startDelay             = 10 * time.Second
		retryInterval          = 2 * time.Second
		scheduleToStartTimeout = 2 * time.Minute
	)

	ctx := &chasm.MockMutableContext{}
	ctx.HandleNow = func(chasm.Component) time.Time { return defaultTime }
	attemptState := &activitypb.ActivityAttemptState{Count: 1}
	activity := &Activity{
		ActivityState: &activitypb.ActivityState{
			ActivityType:           &commonpb.ActivityType{Name: "test-activity-type"},
			RetryPolicy:            defaultRetryPolicy,
			ScheduleToCloseTimeout: durationpb.New(defaultScheduleToCloseTimeout),
			ScheduleToStartTimeout: durationpb.New(scheduleToStartTimeout),
			StartToCloseTimeout:    durationpb.New(defaultStartToCloseTimeout),
			StartDelay:             durationpb.New(startDelay),
			ScheduleTime:           timestamppb.New(defaultTime.Add(-startDelay)),
			Status:                 activitypb.ACTIVITY_EXECUTION_STATUS_STARTED,
			TaskQueue:              &taskqueuepb.TaskQueue{Name: "test-task-queue"},
		},
		LastAttempt: chasm.NewDataField(ctx, attemptState),
		Outcome:     chasm.NewDataField(ctx, &activitypb.ActivityOutcome{}),
	}

	err := TransitionRescheduled.Apply(activity, ctx, rescheduleEvent{
		retryInterval: retryInterval,
		failure:       createStartToCloseTimeoutFailure(),
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, attemptState.Count)

	retryAt := defaultTime.Add(retryInterval)
	require.Equal(t, retryAt, attemptScheduleTimeForRetry(attemptState).AsTime())

	var sawDispatch bool
	for _, task := range ctx.Tasks {
		switch task.Payload.(type) {
		case *activitypb.ActivityDispatchTask:
			sawDispatch = true
			require.Equal(t, retryAt, task.Attributes.ScheduledTime)
		case *activitypb.ScheduleToStartTimeoutTask:
			require.Equal(t, retryAt.Add(scheduleToStartTimeout), task.Attributes.ScheduledTime)
		default:
			t.Fatalf("unexpected task %T", task.Payload)
		}
	}
	require.True(t, sawDispatch)
}

func TestHasEnoughTimeForRetry_IncludesStartDelay(t *testing.T) {
	const (
		startDelay             = 2 * time.Second
		scheduleToCloseTimeout = 3 * time.Second
		retryInterval          = 1 * time.Second
	)
	// Failure shortly after first dispatch. Without extending the deadline by the start delay,
	// schedule time + ScheduleToClose is already in the past relative to now+retryInterval.
	failureTime := defaultTime.Add(startDelay + time.Second)

	ctx := &chasm.MockMutableContext{
		MockContext: chasm.MockContext{
			HandleNow: func(chasm.Component) time.Time { return failureTime },
		},
	}
	activity := &Activity{
		ActivityState: &activitypb.ActivityState{
			ScheduleTime:           timestamppb.New(defaultTime),
			StartDelay:             durationpb.New(startDelay),
			ScheduleToCloseTimeout: durationpb.New(scheduleToCloseTimeout),
			RetryPolicy:            defaultRetryPolicy,
		},
		LastAttempt: chasm.NewDataField(ctx, &activitypb.ActivityAttemptState{Count: 1}),
	}

	ok, interval := activity.hasEnoughTimeForRetry(ctx, retryInterval)
	require.True(t, ok)
	require.Equal(t, retryInterval, interval)
	require.Equal(t, defaultTime.Add(startDelay+scheduleToCloseTimeout), activity.scheduleToCloseDeadline())

	// Same failure time with no start delay is past the unextended deadline.
	activity.StartDelay = nil
	ok, _ = activity.hasEnoughTimeForRetry(ctx, retryInterval)
	require.False(t, ok)
}
