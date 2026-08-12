Added configurable access logging to the reverse tunnel initiator bootstrap extension. The new
``access_log`` field on ``envoy.bootstrap.reverse_tunnel.downstream_socket_interface`` emits
entries for ``handshake_success``, ``handshake_failure``, and ``connection_closed`` lifecycle
events, exposing reverse-tunnel metadata as dynamic metadata under the
``envoy.reverse_tunnel.initiator`` namespace.
