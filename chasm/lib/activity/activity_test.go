package activity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/server/api/historyservice/v1"
	"go.temporal.io/server/chasm"
	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/common/namespace"
	serviceerrors "go.temporal.io/server/common/serviceerror"
	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHandleStarted(t *testing.T) {
	testTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	testRequestID := "test-request-id"
	testStamp := int32(1)

	testCases := []struct {
		name           string
		activityStatus activitypb.ActivityExecutionStatus
		attemptStamp   int32
		requestStamp   int32
		startRequestID string
		requestID      string
		checkOutcome   func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error)
	}{
		{
			name:           "successful transition from scheduled",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED,
			attemptStamp:   testStamp,
			requestStamp:   testStamp,
			requestID:      testRequestID,
			checkOutcome: func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error) {
				require.Equal(t, int32(1), response.Attempt)
				require.NoError(t, err)
			},
		},
		{
			name:           "idempotent retry - same request ID",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_STARTED,
			attemptStamp:   testStamp,
			requestStamp:   testStamp,
			startRequestID: testRequestID,
			requestID:      testRequestID,
			checkOutcome: func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error) {
				require.Equal(t, int32(1), response.Attempt)
				require.NoError(t, err)
			},
		},
		{
			name:           "error - already started with different request ID",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_STARTED,
			attemptStamp:   testStamp,
			requestStamp:   testStamp,
			startRequestID: "different-request-id",
			requestID:      testRequestID,
			checkOutcome: func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error) {
				require.ErrorAs(t, err, new(*serviceerrors.ObsoleteMatchingTask))
			},
		},
		{
			name:           "error - stamp mismatch",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED,
			attemptStamp:   testStamp,
			requestStamp:   testStamp + 1,
			requestID:      testRequestID,
			checkOutcome: func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error) {
				require.ErrorAs(t, err, new(*serviceerrors.ObsoleteMatchingTask))
			},
		},
		{
			name:           "error - invalid transition from completed",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_COMPLETED,
			attemptStamp:   testStamp,
			requestStamp:   testStamp,
			requestID:      testRequestID,
			checkOutcome: func(t *testing.T, response *historyservice.RecordActivityTaskStartedResponse, err error) {
				require.ErrorAs(t, err, new(*serviceerrors.ObsoleteMatchingTask))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock context
			ctx := &chasm.MockMutableContext{
				MockContext: chasm.MockContext{
					HandleNow: func(chasm.Component) time.Time { return testTime },
					HandleExecutionKey: func() chasm.ExecutionKey {
						return chasm.ExecutionKey{
							BusinessID: "test-activity-id",
							RunID:      "test-run-id",
						}
					},
				},
			}

			// Setup activity state
			attemptState := &activitypb.ActivityAttemptState{
				Count:          1,
				Stamp:          tc.attemptStamp,
				StartRequestId: tc.startRequestID,
			}
			if tc.activityStatus == activitypb.ACTIVITY_EXECUTION_STATUS_STARTED {
				attemptState.StartedTime = timestamppb.New(testTime.Add(-1 * time.Minute))
			}

			// Determine heartbeat timeout based on test case
			heartbeatTimeout := 1 * time.Minute
			if tc.name == "successful transition without heartbeat timeout" {
				heartbeatTimeout = 0
			}

			activity := &Activity{
				ActivityState: &activitypb.ActivityState{
					ActivityType:           &commonpb.ActivityType{Name: "test-activity-type"},
					Status:                 tc.activityStatus,
					TaskQueue:              &taskqueuepb.TaskQueue{Name: "test-task-queue"},
					ScheduleToCloseTimeout: durationpb.New(10 * time.Minute),
					ScheduleToStartTimeout: durationpb.New(2 * time.Minute),
					StartToCloseTimeout:    durationpb.New(3 * time.Minute),
					HeartbeatTimeout:       durationpb.New(heartbeatTimeout),
					ScheduleTime:           timestamppb.New(testTime.Add(-30 * time.Second)),
				},
				LastAttempt: chasm.NewDataField(ctx, attemptState),
				RequestData: chasm.NewDataField(ctx, &activitypb.ActivityRequestData{
					Input: &commonpb.Payloads{
						Payloads: []*commonpb.Payload{{Data: []byte("test-input")}},
					},
					Header: &commonpb.Header{
						Fields: map[string]*commonpb.Payload{
							"test-header": {Data: []byte("test-value")},
						},
					},
				}),
				Outcome: chasm.NewDataField(ctx, &activitypb.ActivityOutcome{}),
			}

			// Create request
			request := &historyservice.RecordActivityTaskStartedRequest{
				Stamp:     tc.requestStamp,
				RequestId: tc.requestID,
			}

			// Execute HandleStarted
			response, err := activity.HandleStarted(ctx, request)

			tc.checkOutcome(t, response, err)
		})
	}
}

func TestActivityTerminate(t *testing.T) {
	testCases := []struct {
		name           string
		activityStatus activitypb.ActivityExecutionStatus
		expectErr      string
	}{
		{
			name:           "terminate scheduled activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED,
		},
		{
			name:           "terminate started activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_STARTED,
		},
		{
			name:           "terminate cancel-requested activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_CANCEL_REQUESTED,
		},
		{
			name:           "error on completed activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_COMPLETED,
			expectErr:      "invalid transition from Completed",
		},
		{
			name:           "no-op on already terminated activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_TERMINATED,
		},
		{
			name:           "error on failed activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_FAILED,
			expectErr:      "invalid transition from Failed",
		},
		{
			name:           "error on timed out activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_TIMED_OUT,
			expectErr:      "invalid transition from TimedOut",
		},
		{
			name:           "error on canceled activity",
			activityStatus: activitypb.ACTIVITY_EXECUTION_STATUS_CANCELED,
			expectErr:      "invalid transition from Canceled",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			nsRegistry := namespace.NewMockRegistry(ctrl)
			nsRegistry.EXPECT().GetNamespaceName(gomock.Any()).Return(namespace.Name("test-namespace"), nil).AnyTimes()

			ctx := &chasm.MockMutableContext{
				MockContext: chasm.MockContext{
					HandleNow: func(chasm.Component) time.Time { return defaultTime },
					GoCtx: context.WithValue(context.Background(), ctxKeyActivityContext, &activityContext{
						config: &Config{
							BreakdownMetricsByTaskQueue: dynamicconfig.GetBoolPropertyFnFilteredByTaskQueue(true),
						},
						namespaceRegistry: nsRegistry,
					}),
				},
			}

			activity := &Activity{
				ActivityState: &activitypb.ActivityState{
					ActivityType:           &commonpb.ActivityType{Name: "test-activity-type"},
					Status:                 tc.activityStatus,
					TaskQueue:              &taskqueuepb.TaskQueue{Name: "test-task-queue"},
					ScheduleToCloseTimeout: durationpb.New(10 * time.Minute),
					ScheduleToStartTimeout: durationpb.New(2 * time.Minute),
					StartToCloseTimeout:    durationpb.New(3 * time.Minute),
				},
				LastAttempt: chasm.NewDataField(ctx, &activitypb.ActivityAttemptState{Count: 1}),
				Outcome:     chasm.NewDataField(ctx, &activitypb.ActivityOutcome{}),
			}

			_, err := activity.Terminate(ctx, chasm.TerminateComponentRequest{
				Reason: "Delete activity execution",
			})

			if tc.expectErr != "" {
				require.EqualError(t, err, tc.expectErr)
				require.Equal(t, tc.activityStatus, activity.Status, "expected no state change on error")
			} else {
				require.NoError(t, err)
				require.Equal(t, activitypb.ACTIVITY_EXECUTION_STATUS_TERMINATED, activity.Status)
			}
		})
	}
}

func TestContextMetadata(t *testing.T) {
	t.Run("returns activity type and task queue", func(t *testing.T) {
		ctx := &chasm.MockMutableContext{}
		activity := &Activity{
			ActivityState: &activitypb.ActivityState{
				ActivityType: &commonpb.ActivityType{Name: "my-activity"},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: "my-task-queue"},
			},
		}

		md := activity.ContextMetadata(ctx)
		require.Equal(t, map[string]string{
			"standalone-activity-type":       "my-activity",
			"standalone-activity-task-queue": "my-task-queue",
		}, md)
	})

	t.Run("returns only activity type when task queue is empty", func(t *testing.T) {
		ctx := &chasm.MockMutableContext{}
		activity := &Activity{
			ActivityState: &activitypb.ActivityState{
				ActivityType: &commonpb.ActivityType{Name: "my-activity"},
			},
		}

		md := activity.ContextMetadata(ctx)
		require.Equal(t, map[string]string{
			"standalone-activity-type": "my-activity",
		}, md)
	})

	t.Run("returns only task queue when activity type is empty", func(t *testing.T) {
		ctx := &chasm.MockMutableContext{}
		activity := &Activity{
			ActivityState: &activitypb.ActivityState{
				TaskQueue: &taskqueuepb.TaskQueue{Name: "my-task-queue"},
			},
		}

		md := activity.ContextMetadata(ctx)
		require.Equal(t, map[string]string{
			"standalone-activity-task-queue": "my-task-queue",
		}, md)
	})

	t.Run("returns nil when both are empty", func(t *testing.T) {
		ctx := &chasm.MockMutableContext{}
		activity := &Activity{
			ActivityState: &activitypb.ActivityState{},
		}

		md := activity.ContextMetadata(ctx)
		require.Nil(t, md)
	})
}

// TestHasEnoughTimeForRetry verifies that the schedule-to-close deadline used to gate retries is
// measured from schedule_time + start_delay, so a start delay does not cause retries to be
// rejected early by roughly the delay duration.
func TestHasEnoughTimeForRetry(t *testing.T) {
	testCases := []struct {
		name                    string
		startDelay              time.Duration
		scheduleToCloseTimeout  time.Duration
		now                     time.Time
		overridingRetryInterval time.Duration
		expectEnoughTime        bool
	}{
		{
			name:                    "without start delay - retry rejected near schedule-to-close boundary",
			startDelay:              0,
			scheduleToCloseTimeout:  10 * time.Minute,
			now:                     defaultTime.Add(9 * time.Minute),
			overridingRetryInterval: 2 * time.Minute,
			expectEnoughTime:        false,
		},
		{
			name:                    "start delay extends the deadline, allowing the same retry",
			startDelay:              5 * time.Minute,
			scheduleToCloseTimeout:  10 * time.Minute,
			now:                     defaultTime.Add(9 * time.Minute),
			overridingRetryInterval: 2 * time.Minute,
			expectEnoughTime:        true,
		},
		{
			name:                    "no schedule-to-close timeout - always enough time",
			startDelay:              0,
			scheduleToCloseTimeout:  0,
			now:                     defaultTime.Add(365 * 24 * time.Hour),
			overridingRetryInterval: 2 * time.Minute,
			expectEnoughTime:        true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &chasm.MockMutableContext{
				MockContext: chasm.MockContext{
					HandleNow: func(chasm.Component) time.Time { return tc.now },
				},
			}
			activity := &Activity{
				ActivityState: &activitypb.ActivityState{
					ScheduleToCloseTimeout: durationpb.New(tc.scheduleToCloseTimeout),
					ScheduleTime:           timestamppb.New(defaultTime),
					StartDelay:             durationpb.New(tc.startDelay),
				},
				LastAttempt: chasm.NewDataField(ctx, &activitypb.ActivityAttemptState{Count: 1}),
			}

			enoughTime, _ := activity.hasEnoughTimeForRetry(ctx, tc.overridingRetryInterval)
			require.Equal(t, tc.expectEnoughTime, enoughTime)
		})
	}
}

// TestAttemptScheduleTime verifies that the first attempt's scheduled time reflects the start
// delay, while retries (computed from attemptScheduleTimeForRetry) do not re-apply it.
func TestAttemptScheduleTime(t *testing.T) {
	startDelay := 90 * time.Second
	activity := &Activity{
		ActivityState: &activitypb.ActivityState{
			ScheduleTime: timestamppb.New(defaultTime),
			StartDelay:   durationpb.New(startDelay),
		},
	}

	t.Run("first attempt includes start delay", func(t *testing.T) {
		attempt := &activitypb.ActivityAttemptState{Count: 1}
		scheduledTime := activity.attemptScheduleTime(attempt)
		require.Equal(t, defaultTime.Add(startDelay), scheduledTime.AsTime())
	})

	t.Run("retry attempt does not re-apply start delay", func(t *testing.T) {
		completeTime := defaultTime.Add(5 * time.Minute)
		attempt := &activitypb.ActivityAttemptState{
			Count:                2,
			CompleteTime:         timestamppb.New(completeTime),
			CurrentRetryInterval: durationpb.New(time.Minute),
		}
		scheduledTime := activity.attemptScheduleTime(attempt)
		require.Equal(t, completeTime.Add(time.Minute), scheduledTime.AsTime())
	})
}
