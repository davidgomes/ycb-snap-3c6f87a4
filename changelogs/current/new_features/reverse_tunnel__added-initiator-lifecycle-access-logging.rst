Added configurable access logging to the reverse tunnel initiator bootstrap extension via the
:ref:`access_log
<envoy_v3_api_field_extensions.bootstrap.reverse_tunnel.downstream_socket_interface.v3.DownstreamReverseConnectionSocketInterface.access_log>`
field. Log entries are emitted for ``handshake_success``, ``handshake_failure`` and
``connection_closed`` lifecycle events, with reverse-tunnel metadata exposed as dynamic metadata
under the ``envoy.reverse_tunnel.initiator`` namespace.
