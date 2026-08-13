Added support for access logging on the reverse tunnel initiator. The
:ref:`access_log <envoy_v3_api_field_extensions.bootstrap.reverse_tunnel.downstream_socket_interface.v3.DownstreamReverseConnectionSocketInterface.access_log>`
field on the downstream reverse connection socket interface configures access loggers that emit
entries for ``handshake_success``, ``handshake_failure``, and ``connection_closed`` lifecycle
events, exposing reverse-tunnel metadata as dynamic metadata under the
``envoy.reverse_tunnel.initiator`` namespace.
