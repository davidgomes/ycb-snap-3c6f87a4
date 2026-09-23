package activity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/api/historyservice/v1"
	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
)

type resetActivityCapture struct {
	historyservice.UnimplementedHistoryServiceServer
	req *historyservice.ResetActivityRequest
}

func (r *resetActivityCapture) ResetActivity(
	_ context.Context,
	req *historyservice.ResetActivityRequest,
) (*historyservice.ResetActivityResponse, error) {
	r.req = req
	return &historyservice.ResetActivityResponse{}, nil
}

func TestResetActivityExecutionForwardsResetHeartbeat(t *testing.T) {
	for _, clear := range []bool{false, true} {
		capture := &resetActivityCapture{}
		h := newHandler(nil, capture, nil, nil, nil, nil)
		_, err := h.ResetActivityExecution(context.Background(), &activitypb.ResetActivityExecutionRequest{
			NamespaceId: "ns",
			FrontendRequest: &workflowservice.ResetActivityExecutionRequest{
				Namespace:      "ns",
				WorkflowId:     "wf",
				ActivityId:     "act",
				RunId:          "run",
				ResetHeartbeat: clear,
			},
		})
		require.NoError(t, err)
		require.Equal(t, clear, capture.req.GetFrontendRequest().GetResetHeartbeat())
		require.Equal(t, "act", capture.req.GetFrontendRequest().GetActivity().(*workflowservice.ResetActivityRequest_Id).Id)
		require.Equal(t, "wf", capture.req.GetFrontendRequest().GetExecution().GetWorkflowId())
	}
}
