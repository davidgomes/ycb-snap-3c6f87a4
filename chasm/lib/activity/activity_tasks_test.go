package activity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/server/chasm"
	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestActivityDispatchTaskHandlerValidate verifies that a deferred dispatch task (i.e. one
// scheduled after a start delay) is only pushed to matching while the activity is still
// SCHEDULED. If a cancel or terminate request lands during the delay, the activity transitions
// away from SCHEDULED immediately, and the dispatch task must be invalidated once it fires,
// regardless of how much of the delay has elapsed.
func TestActivityDispatchTaskHandlerValidate(t *testing.T) {
	newActivity := func(status activitypb.ActivityExecutionStatus, stamp int32) (*Activity, *chasm.MockMutableContext) {
		ctx := &chasm.MockMutableContext{}
		attemptState := &activitypb.ActivityAttemptState{Count: 1, Stamp: stamp}
		activity := &Activity{
			ActivityState: &activitypb.ActivityState{
				ActivityType: &commonpb.ActivityType{Name: "test-activity-type"},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: "test-task-queue"},
				Status:       status,
				StartDelay:   durationpb.New(30 * time.Second),
			},
			LastAttempt: chasm.NewDataField(ctx, attemptState),
		}
		return activity, ctx
	}

	h := newActivityDispatchTaskHandler(activityDispatchTaskHandlerOptions{})

	t.Run("valid while still scheduled with matching stamp", func(t *testing.T) {
		activity, ctx := newActivity(activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED, 1)
		ok, err := h.Validate(ctx, activity, chasm.TaskAttributes{}, &activitypb.ActivityDispatchTask{Stamp: 1})
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("invalid once canceled during the delay", func(t *testing.T) {
		activity, ctx := newActivity(activitypb.ACTIVITY_EXECUTION_STATUS_CANCELED, 1)
		ok, err := h.Validate(ctx, activity, chasm.TaskAttributes{}, &activitypb.ActivityDispatchTask{Stamp: 1})
		require.NoError(t, err)
		require.False(t, ok, "dispatch must not proceed once the activity is canceled during the delay")
	})

	t.Run("invalid once terminated during the delay", func(t *testing.T) {
		activity, ctx := newActivity(activitypb.ACTIVITY_EXECUTION_STATUS_TERMINATED, 1)
		ok, err := h.Validate(ctx, activity, chasm.TaskAttributes{}, &activitypb.ActivityDispatchTask{Stamp: 1})
		require.NoError(t, err)
		require.False(t, ok, "dispatch must not proceed once the activity is terminated during the delay")
	})

	t.Run("invalid on stamp mismatch", func(t *testing.T) {
		activity, ctx := newActivity(activitypb.ACTIVITY_EXECUTION_STATUS_SCHEDULED, 2)
		ok, err := h.Validate(ctx, activity, chasm.TaskAttributes{}, &activitypb.ActivityDispatchTask{Stamp: 1})
		require.NoError(t, err)
		require.False(t, ok)
	})
}
