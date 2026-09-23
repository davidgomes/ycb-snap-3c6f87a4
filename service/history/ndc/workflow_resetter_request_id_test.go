package ndc

import (
	"testing"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	persistencespb "go.temporal.io/server/api/persistence/v1"
)

func TestOriginalStartRequestID(t *testing.T) {
	started := &persistencespb.RequestIDInfo{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, EventId: 1}
	optionsUpdated := &persistencespb.RequestIDInfo{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_OPTIONS_UPDATED, EventId: 5}

	t.Run("first run", func(t *testing.T) {
		state := &persistencespb.WorkflowExecutionState{
			CreateRequestId: "original",
			RequestIds:      map[string]*persistencespb.RequestIDInfo{"original": started, "attached": optionsUpdated},
		}
		require.Equal(t, "original", originalStartRequestID(state, "reset"))
	})

	t.Run("reset run", func(t *testing.T) {
		state := &persistencespb.WorkflowExecutionState{
			CreateRequestId: "reset-1",
			RequestIds:      map[string]*persistencespb.RequestIDInfo{"original": started, "attached": optionsUpdated},
		}
		require.Equal(t, "original", originalStartRequestID(state, "reset-2"))
	})

	t.Run("no request IDs", func(t *testing.T) {
		require.Equal(t, "reset", originalStartRequestID(&persistencespb.WorkflowExecutionState{}, "reset"))
	})
}
