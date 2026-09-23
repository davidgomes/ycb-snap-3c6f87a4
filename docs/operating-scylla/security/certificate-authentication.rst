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


Accepting Certificate and Password Clients on the Same Port
-----------------------------------------------------------

``CertificateAuthenticator`` requires every client to authenticate with a certificate, while ``PasswordAuthenticator``
requires every client to authenticate with a username and password. To migrate between the two without a hard cutover,
use ``com.scylladb.auth.CertificateOrPasswordAuthenticator``, which accepts both kinds of clients on the same CQL port:

* A client that presents a trusted TLS client certificate is authenticated from the certificate, with its role extracted
  using ``auth_certificate_role_queries``, exactly as with ``CertificateAuthenticator``. It is not asked for a username
  and password.
* A client that does not present a certificate, whether it connects over TLS or to an unencrypted CQL port, authenticates
  with a username and password, exactly as with ``PasswordAuthenticator``.

A client that presents a certificate is authenticated only by that certificate. If the certificate is not trusted, the
TLS handshake fails. If no role can be extracted from the certificate, authentication fails. In neither case does
ScyllaDB fall back to the client's username and password.

#. Enable authentication

   Enable authentication and define roles as described in the :doc:`Enable Authentication </operating-scylla/security/authentication/>` document.
   Roles used by certificate clients only need ``LOGIN = true``. Roles used by password clients also need a password.

#. Enable CQL transport TLS with optional client certificate verification

   In each node, configure the ``client_encryption_options`` option in the ``/etc/scylla/scylla.yaml`` file:

   .. code-block:: yaml

      client_encryption_options:
         enabled: True
         certificate: <server cert>
         keyfile: <server key>
         truststore: <shared trust>
         require_client_auth: optional

   With ``require_client_auth: optional``, ScyllaDB asks TLS clients for a certificate but does not require one.
   Certificates that clients do present are verified against the trust store.

   .. warning:: Do not set ``require_client_auth: True``. It makes the TLS handshake fail for every client that does not
      present a certificate, so password clients cannot connect over TLS.

#. Configure the certificate or password authenticator in each node

   .. code-block:: yaml

      authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
      auth_certificate_role_queries:
         - source: SUBJECT
           query: CN=([^,\s]+)

   Restart the cluster.

.. note:: Password clients see ``com.scylladb.auth.CertificateOrPasswordAuthenticator`` as the server's authenticator
   class name. If a client driver is configured with an explicit list of allowed authenticator class names (for example,
   gocql's ``AllowedAuthenticators``), add this name to it.


Additional Resources
--------------------

* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>` 
* :doc:`Authorization </operating-scylla/security/authorization/>` 



