package activity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/common/log"
	"go.temporal.io/server/common/namespace"
	"go.temporal.io/server/common/testing/protorequire"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
)

type recordingActivityServiceClient struct {
	activitypb.ActivityServiceClient
	startRequests []*activitypb.StartActivityExecutionRequest
}

func (c *recordingActivityServiceClient) StartActivityExecution(
	_ context.Context,
	req *activitypb.StartActivityExecutionRequest,
	_ ...grpc.CallOption,
) (*activitypb.StartActivityExecutionResponse, error) {
	c.startRequests = append(c.startRequests, req)
	return &activitypb.StartActivityExecutionResponse{
		FrontendResponse: &workflowservice.StartActivityExecutionResponse{},
	}, nil
}

func TestStartActivityExecutionStartDelay(t *testing.T) {
	testCases := []struct {
		name              string
		startDelayEnabled bool
		startDelay        *durationpb.Duration
		expectRejected    bool
	}{
		{
			name:              "disabled rejects positive start delay",
			startDelayEnabled: false,
			startDelay:        durationpb.New(time.Minute),
			expectRejected:    true,
		},
		{
			name:              "disabled allows nil start delay",
			startDelayEnabled: false,
			startDelay:        nil,
		},
		{
			name:              "disabled allows zero start delay",
			startDelayEnabled: false,
			startDelay:        durationpb.New(0),
		},
		{
			name:              "enabled forwards positive start delay",
			startDelayEnabled: true,
			startDelay:        durationpb.New(time.Minute),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			nsRegistry := namespace.NewMockRegistry(ctrl)
			if !tc.expectRejected {
				nsRegistry.EXPECT().GetNamespaceID(namespace.Name("test-namespace")).Return(namespace.ID("test-namespace-id"), nil)
			}
			client := &recordingActivityServiceClient{}
			h := &frontendHandler{
				client: client,
				config: &Config{
					BlobSizeLimitError:         defaultBlobSizeLimitError,
					BlobSizeLimitWarn:          defaultBlobSizeLimitWarn,
					DefaultActivityRetryPolicy: getDefaultRetrySettings,
					Enabled:                    dynamicconfig.GetBoolPropertyFnFilteredByNamespace(true),
					MaxIDLengthLimit:           func() int { return defaultMaxIDLengthLimit },
					StartDelayEnabled:          dynamicconfig.GetBoolPropertyFnFilteredByNamespace(tc.startDelayEnabled),
				},
				logger:            log.NewNoopLogger(),
				namespaceRegistry: nsRegistry,
			}

			_, err := h.StartActivityExecution(context.Background(), &workflowservice.StartActivityExecutionRequest{
				Namespace:           "test-namespace",
				ActivityId:          "test-activity",
				ActivityType:        &commonpb.ActivityType{Name: "test-type"},
				TaskQueue:           &taskqueuepb.TaskQueue{Name: "test-queue"},
				StartToCloseTimeout: durationpb.New(time.Minute),
				StartDelay:          tc.startDelay,
			})

			if tc.expectRejected {
				var invalidArgErr *serviceerror.InvalidArgument
				require.ErrorAs(t, err, &invalidArgErr)
				require.Empty(t, client.startRequests)
				return
			}
			require.NoError(t, err)
			require.Len(t, client.startRequests, 1)
			protorequire.ProtoEqual(t, tc.startDelay, client.startRequests[0].GetFrontendRequest().GetStartDelay())
		})
	}
}

type hasRequestID interface {
	GetRequestId() string
}

// TestRequestIdStableAcrossRetries verifies that a request ID is re-used
// across retries, even if server-generated.
func TestRequestIdStableAcrossRetries(t *testing.T) {
	h := &frontendHandler{
		config: &Config{
			BlobSizeLimitError:         defaultBlobSizeLimitError,
			BlobSizeLimitWarn:          defaultBlobSizeLimitWarn,
			MaxIDLengthLimit:           func() int { return defaultMaxIDLengthLimit },
			DefaultActivityRetryPolicy: getDefaultRetrySettings,
		},
		logger: log.NewNoopLogger(),
	}
	nsID := namespace.ID("test-namespace-id")

	newReq := func(requestId string) *workflowservice.StartActivityExecutionRequest {
		return &workflowservice.StartActivityExecutionRequest{
			Namespace:  "test-namespace",
			ActivityId: "test-activity",
			ActivityType: &commonpb.ActivityType{
				Name: "test-type",
			},
			TaskQueue: &taskqueuepb.TaskQueue{
				Name: "test-queue",
			},
			StartToCloseTimeout: durationpb.New(time.Minute),
			RequestId:           requestId,
		}
	}

	// Simulate two RetryableInterceptor attempts: both call
	// validateAndPopulateStartRequest with the same request pointer.
	validateTwoAttempts := func(t *testing.T, req *workflowservice.StartActivityExecutionRequest) {
		t.Helper()
		clone1, err := h.validateAndPopulateStartRequest(req, nsID)
		require.NoError(t, err)
		require.NotEmpty(t, clone1.RequestId)

		clone2, err := h.validateAndPopulateStartRequest(req, nsID)
		require.NoError(t, err)
		require.Equal(t, clone1.RequestId, clone2.RequestId)
	}

	// validateTwice calls validate twice and asserts the request ID is stable.
	validateTwice := func(t *testing.T, req hasRequestID, validate func() error) {
		t.Helper()
		require.NoError(t, validate())
		require.NotEmpty(t, req.GetRequestId())
		firstID := req.GetRequestId()
		require.NoError(t, validate())
		require.Equal(t, firstID, req.GetRequestId())
	}

	t.Run("start/server-generated", func(t *testing.T) {
		validateTwoAttempts(t, newReq(""))
	})

	t.Run("start/client-provided", func(t *testing.T) {
		validateTwoAttempts(t, newReq("my-request-id"))
	})

	t.Run("terminate/server-generated", func(t *testing.T) {
		req := &workflowservice.TerminateActivityExecutionRequest{
			Namespace:  "test-namespace",
			ActivityId: "test-activity",
		}
		validateTwice(t, req, func() error {
			return validateAndNormalizeTerminateRequest(
				req, defaultMaxIDLengthLimit, defaultBlobSizeLimitError, defaultBlobSizeLimitWarn, log.NewNoopLogger())
		})
	})

	t.Run("cancel/server-generated", func(t *testing.T) {
		req := &workflowservice.RequestCancelActivityExecutionRequest{
			Namespace:  "test-namespace",
			ActivityId: "test-activity",
		}
		validateTwice(t, req, func() error {
			return validateAndNormalizeCancelRequest(
				req, defaultMaxIDLengthLimit, defaultBlobSizeLimitError, defaultBlobSizeLimitWarn, log.NewNoopLogger())
		})
	})
}
