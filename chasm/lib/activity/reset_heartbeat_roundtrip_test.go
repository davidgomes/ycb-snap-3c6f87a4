package activity

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"go.temporal.io/server/chasm/lib/activity/gen/activitypb/v1"
)

func TestResetShouldClearHeartbeatRoundTrip(t *testing.T) {
	in := &activitypb.ActivityState{ResetShouldClearHeartbeat: true, LastResetRequestId: "req"}
	b, err := proto.Marshal(in)
	require.NoError(t, err)
	out := &activitypb.ActivityState{}
	require.NoError(t, proto.Unmarshal(b, out))
	require.True(t, out.GetResetShouldClearHeartbeat())
	require.Equal(t, "req", out.GetLastResetRequestId())
}
