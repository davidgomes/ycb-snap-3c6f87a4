package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	persistencespb "go.temporal.io/server/api/persistence/v1"
)

func TestGetStartRequestID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		executionState *persistencespb.WorkflowExecutionState
		expected       string
	}{
		{
			name:           "nil execution state",
			executionState: nil,
			expected:       "",
		},
		{
			name: "no request IDs falls back to create request ID",
			executionState: &persistencespb.WorkflowExecutionState{
				CreateRequestId: "create-request-id",
			},
			expected: "create-request-id",
		},
		{
			name: "start request ID differs from create request ID after reset",
			executionState: &persistencespb.WorkflowExecutionState{
				CreateRequestId: "reset-request-id",
				RequestIds: map[string]*persistencespb.RequestIDInfo{
					"attached-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_OPTIONS_UPDATED,
						EventId:   5,
					},
					"start-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
						EventId:   1,
					},
				},
			},
			expected: "start-request-id",
		},
		{
			name: "create request ID tracked as start request ID",
			executionState: &persistencespb.WorkflowExecutionState{
				CreateRequestId: "create-request-id",
				RequestIds: map[string]*persistencespb.RequestIDInfo{
					"create-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
						EventId:   1,
					},
				},
			},
			expected: "create-request-id",
		},
		{
			name: "reset request ID tracked alongside original start request ID",
			executionState: &persistencespb.WorkflowExecutionState{
				CreateRequestId: "reset-request-id",
				RequestIds: map[string]*persistencespb.RequestIDInfo{
					"reset-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
						EventId:   1,
					},
					"start-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
						EventId:   1,
					},
				},
			},
			expected: "start-request-id",
		},
		{
			name: "only attached request IDs falls back to create request ID",
			executionState: &persistencespb.WorkflowExecutionState{
				CreateRequestId: "create-request-id",
				RequestIds: map[string]*persistencespb.RequestIDInfo{
					"attached-request-id": {
						EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_OPTIONS_UPDATED,
						EventId:   5,
					},
				},
			},
			expected: "create-request-id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.expected, GetStartRequestID(tc.executionState))
		})
	}
}
