Certificate or Password Authentication
======================================

``CertificateOrPasswordAuthenticator`` lets one CQL port accept both kinds of client during a migration:

* A client that presents a trusted TLS client certificate is logged in from that certificate. There is no SASL username/password exchange.
* A client that presents no certificate uses the usual username/password SASL exchange. That includes a TLS connection with optional client authentication, and a plain non-TLS CQL connection.

A certificate that is presented is the only authentication path for that connection. If the certificate is trusted but its subject or SAN matches no ``auth_certificate_role_queries`` expression, authentication fails. The server does not then accept a password on that connection. A certificate that is not trusted fails the TLS handshake, before password authentication can run.

Procedure
---------

#. Enable authentication and authorization, and create roles, as described in :doc:`Enable Authentication </operating-scylla/security/authentication/>` and :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>`.

   Password clients need ``LOGIN = true`` and a password (``CREATE ROLE ... WITH PASSWORD = '...' AND LOGIN = true``).

   Certificate clients need a role whose name is extracted from the certificate, with ``LOGIN = true``. A password is optional for those roles. The same role can have a password, so one identity can connect either way while clients are migrated.

#. On each node, select the authenticator and the role-extraction queries. The queries are the same subject and SAN expressions used by :doc:`CertificateAuthenticator </operating-scylla/security/certificate-authentication/>`. The first matching expression wins, and the expression must contain one capture group.

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
      auth_certificate_role_queries:
        - source: SUBJECT
          query: CN=([^,\s]+)

   ``CertificateOrPasswordAuthenticator`` is also accepted as the short name. The qualified name is ``com.scylladb.auth.CertificateOrPasswordAuthenticator``.

#. Enable client-to-node TLS and **ask** for a client certificate. Use ``require_client_auth: optional``.

   .. code-block:: yaml

      client_encryption_options:
          enabled: true
          certificate: /etc/scylla/db.crt
          keyfile: /etc/scylla/db.key
          truststore: /etc/scylla/cadb.pem
          require_client_auth: optional

   ``optional`` maps to TLS client-auth ``REQUEST``: the server asks for a certificate, validates it when the client sends one, and still completes the handshake when the client sends none. Password-only clients connect only if they do not present a certificate.

   ``require_client_auth: true`` requires a certificate from every TLS client. Password-only clients are rejected during the handshake and never reach SASL. Use ``true`` only when every client on that port authenticates with a certificate.

   ``require_client_auth: false`` does not request a client certificate, so certificate authentication does not run and every connection uses a password.

   Put the CA that signs client certificates in ``truststore`` (or in the system trust store when ``truststore`` is unset). Sign each certificate client so the configured query yields its role name. A typical choice is the subject common name.

#. Restart the node.

   .. include:: /rst_include/scylla-commands-restart-index.rst

Clients
-------

Password clients use the normal username/password SASL mechanism (PLAIN), the same way they do with ``PasswordAuthenticator``. Configure the driver with a username and password. Do not give those clients a TLS client certificate: if a certificate is presented, the server authenticates only from that certificate.

Certificate clients present a trusted client certificate and do not send a password. On success the server completes startup without a SASL exchange.

A wrong password is a CQL authentication error. It is not a TLS failure. A certificate signed by an unknown CA fails the TLS handshake and the connection is closed. A trusted certificate whose subject and SAN match no role query fails CQL authentication and is not accepted with a password instead.

Plain CQL port
--------------

To keep a non-TLS port next to the TLS port, set ``native_transport_port_ssl`` to a different port from ``native_transport_port`` and enable ``client_encryption_options``. The TLS port uses the certificate-or-password rules above. The plain port has no client certificate, so it authenticates with username and password.

Additional Resources
--------------------

* :doc:`Certificate Based Authentication </operating-scylla/security/certificate-authentication/>`
* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Encryption: Data in Transit Client to Node </operating-scylla/security/client-node-encryption/>`
