Added :ref:`suppress_client_ca_list
<envoy_v3_api_field_extensions.transport_sockets.tls.v3.CertificateValidationContext.suppress_client_ca_list>`
to omit trusted CA distinguished names from the downstream TLS ``CertificateRequest`` while still
validating presented client certificates against the configured trust bundle.
