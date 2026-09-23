Added :ref:`suppress_client_ca_list
<envoy_v3_api_field_extensions.transport_sockets.tls.v3.CertificateValidationContext.suppress_client_ca_list>`
to omit the trusted CA names from the TLS ``CertificateRequest`` sent to downstream clients, while still
validating presented client certificates against the configured trusted CAs. This avoids handshake
failures with clients that cannot handle a large advertised CA list. Supported by the default and
SPIFFE certificate validators.
