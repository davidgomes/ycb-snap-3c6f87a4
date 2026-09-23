Certificate-based Authentication
================================

Certificate-based authentication is an alternative method of resolving a login to credentials, i.e. a defined ScyllaDB role.
Instead of defining one or more user/password pairs, each defined role is assigned depending on verified 
client certificate Subject/Alt information.


Procedure
---------

#. Enable authentication

   Enable authentication and define authorized roles in the cluster as described in the :doc:`Enable Authentication </operating-scylla/security/authentication/>` document. 

#. Enable CQL transport TLS using client certificate verification
   
   Enable CQL transport TLS using client certificate verification in each node by configuring the `` client_encryption_options`` option in the ``/etc/scylla/scylla.yaml`` file:

   .. code-block:: yaml

      client_encryption_options:
         enabled: True
         certificate: <server cert>
         keyfile: <server key>
         truststore: <shared trust>
         require_client_auth: True

   Create the client certificates such that each one designates the desired login role for this connection. A typical method 
   would be to assign the subject common name as the role name. All client certificates need to be signed in such a way that 
   either system trust or the defined trust store can verify them.

   ``require_client_auth: True`` rejects any client that does not present a certificate, including password-only clients, during the TLS handshake. To let both kinds of client share one port, use ``optional`` and ``CertificateOrPasswordAuthenticator`` as described below.

#. Configure the certificate based authenticator in each node.

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateAuthenticator
      auth_certificate_role_queries:
               - source: SUBJECT
               query: CN=([^,\s]+)

   More than one expression can be used to extract role names. The first matching expression will be used. The available sources are `SUBJECT` (distinguished name) and
   `ALTNAME` (subject extensions). The query should be a regular expression with a single match clause.

   Restart the cluster.

Certificate or password on one port
------------------------------------

``CertificateAuthenticator`` and ``PasswordAuthenticator`` cannot be selected together, so moving from passwords to certificates otherwise requires a cutover: legacy password clients and certificate clients cannot use the same CQL endpoint. ``com.scylladb.auth.CertificateOrPasswordAuthenticator`` (short name ``CertificateOrPasswordAuthenticator``) accepts either credential on one port.

* A trusted client certificate is authenticated from ``auth_certificate_role_queries`` (subject and SAN), with no SASL username/password exchange.
* No client certificate — TLS with ``require_client_auth: optional``, or a plain non-TLS CQL connection — uses the same username/password SASL exchange as ``PasswordAuthenticator``. A wrong password is an authentication failure. The TLS session itself is already established.
* A certificate that was accepted by TLS but matches no role query fails authentication. The server does not try a password for that connection.
* A certificate the trust store does not accept fails the TLS handshake. Password authentication never runs for that connection.

Use ``optional``, not ``true``. ``true`` (require) closes the handshake when the client has no certificate, so password-only clients never reach authentication.

#. Enable authentication and authorization, and create password roles, as for :doc:`password authentication </operating-scylla/security/authentication/>`.

   Password roles are stored the same way as with ``PasswordAuthenticator``:

   .. code-block:: cql

      CREATE ROLE password_user WITH PASSWORD = '...' AND LOGIN = true;

   Certificate-only roles do not need a password. They still need ``LOGIN`` and a name that a role query can extract:

   .. code-block:: cql

      CREATE ROLE cert_user WITH LOGIN = true;

#. Enable CQL transport TLS and **request** a client certificate without requiring one:

   .. code-block:: yaml

      client_encryption_options:
        enabled: true
        certificate: <server cert>
        keyfile: <server key>
        truststore: <shared trust>
        require_client_auth: optional

   ``optional`` asks for a certificate during the handshake and validates it when the client sends one. Clients that send none continue, and then authenticate with a username and password. ``require_client_auth: true`` is the wrong setting for this authenticator: those password-only clients are disconnected before they can log in.

   Client certificates must be signed by a CA in the trust store (or the system trust store when ``truststore`` is unset). An untrusted certificate is rejected by TLS.

#. Select the authenticator and the same role queries used by certificate authentication:

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
      auth_certificate_role_queries:
        - source: SUBJECT
          query: CN=([^,\s]+)

   The short name ``CertificateOrPasswordAuthenticator`` is equivalent. More than one expression can be used. The first match wins. Sources are ``SUBJECT`` (distinguished name) and ``ALTNAME`` (subject alternative names). Each query is a regular expression with a single capture group, which becomes the role name.

#. Restart the cluster.

   Password clients use the same SASL PLAIN username/password configuration as with ``PasswordAuthenticator`` (for example cqlsh, or a driver plain-text auth provider). They must not be required to present a client certificate. Certificate clients present a trusted certificate and do not send a password. The server reports the authenticator class as ``com.scylladb.auth.CertificateOrPasswordAuthenticator``.


Additional Resources
--------------------

* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>` 
* :doc:`Authorization </operating-scylla/security/authorization/>` 



