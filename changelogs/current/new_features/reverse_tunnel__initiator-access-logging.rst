Added support for configurable access logging on the reverse tunnel initiator bootstrap extension via the new
:ref:`access_log
<envoy_v3_api_field_extensions.bootstrap.reverse_tunnel.downstream_socket_interface.v3.DownstreamReverseConnectionSocketInterface.access_log>`
field. Access log entries are emitted for reverse tunnel handshake success, handshake failure, and connection close
events, with reverse-tunnel metadata exposed as dynamic metadata under the ``envoy.reverse_tunnel.initiator``
namespace.
