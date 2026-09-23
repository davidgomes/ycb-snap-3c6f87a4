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

#. Configure the certificate based authenticator in each node.

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateAuthenticator
      auth_certificate_role_queries:
               - source: SUBJECT
               query: CN=([^,\s]+)

   More than one expression can be used to extract role names. The first matching expression will be used. The available sources are `SUBJECT` (distinguished name) and
   `ALTNAME` (subject extensions). The query should be a regular expression with a single match clause.

   Restart the cluster.


Combining Certificate and Password Authentication
-------------------------------------------------

``com.scylladb.auth.CertificateOrPasswordAuthenticator`` lets the same CQL port accept both certificate-based and
password-based clients. This is useful when migrating from password authentication to certificate authentication,
since existing password clients and new certificate clients can connect to the same endpoint at the same time.

For each connection:

* If the client presents a TLS client certificate that the node trusts, the role is taken from the certificate using
  ``auth_certificate_role_queries``, exactly as with ``CertificateAuthenticator``. No username/password exchange takes place.
* If the client does not present a certificate (a TLS connection without a client certificate, or a plain non-TLS
  connection), the client authenticates with username and password, exactly as with ``PasswordAuthenticator``.
* If the client presents a certificate, authentication is decided by the certificate alone. A trusted certificate that
  does not match any role query fails authentication; it does not fall back to password authentication. A certificate
  that the node does not trust is rejected during the TLS handshake.

To enable it:

#. Enable authentication and create the roles as described in the :doc:`Enable Authentication </operating-scylla/security/authentication/>` document.
   Password clients need roles with a password; certificate clients need roles whose names match the role queries.

#. Enable CQL transport TLS with *optional* client certificate verification in each node:

   .. code-block:: yaml

      client_encryption_options:
         enabled: True
         certificate: <server cert>
         keyfile: <server key>
         truststore: <shared trust>
         require_client_auth: optional

   With ``require_client_auth: optional`` the node requests a client certificate during the TLS handshake and verifies it
   if one is presented, but still accepts clients that do not present one. Do **not** use ``require_client_auth: True``
   with this authenticator unless every client has a certificate: in that mode, TLS clients without a certificate are
   rejected during the handshake and can never reach password authentication.

#. Configure the authenticator and role queries in each node:

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
      auth_certificate_role_queries:
               - source: SUBJECT
               query: CN=([^,\s]+)

   Restart the cluster.


Additional Resources
--------------------

* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>` 
* :doc:`Authorization </operating-scylla/security/authorization/>` 



