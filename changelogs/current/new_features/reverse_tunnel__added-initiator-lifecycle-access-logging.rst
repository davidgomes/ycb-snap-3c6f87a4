Added configurable access logging to the reverse tunnel initiator bootstrap extension via the new
:ref:`access_log
<envoy_v3_api_field_extensions.bootstrap.reverse_tunnel.downstream_socket_interface.v3.DownstreamReverseConnectionSocketInterface.access_log>`
field. Access log entries are emitted when a reverse tunnel handshake succeeds or fails and when an
established reverse tunnel connection is closed, with reverse-tunnel metadata exposed as dynamic
metadata under the ``envoy.reverse_tunnel.initiator`` namespace.
