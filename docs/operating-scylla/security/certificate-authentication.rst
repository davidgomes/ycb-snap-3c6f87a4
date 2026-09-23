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

Certificate or password on one port
------------------------------------

``CertificateAuthenticator`` accepts only a client certificate. Moving a cluster from password authentication to certificates then requires a hard cutover: password clients and certificate clients cannot share the same CQL endpoint.

``com.scylladb.auth.CertificateOrPasswordAuthenticator`` (short name ``CertificateOrPasswordAuthenticator``) serves both on one port:

* A trusted TLS client certificate is authenticated with the same ``auth_certificate_role_queries`` subject and SAN rules as ``CertificateAuthenticator``. No SASL username/password exchange is performed.
* A connection with no client certificate uses username/password SASL, including a plain (non-TLS) CQL port.
* A certificate that is presented but does not map to a role fails authentication. The server does not try a password for that connection.
* A certificate the trust store does not accept fails during the TLS handshake, before password authentication can run.

Enable it on each node. Set ``require_client_auth`` to ``optional`` (request a certificate, do not require one). ``true`` rejects password-only clients at the handshake, so they never reach password authentication. ``false`` does not ask for a certificate, so every TLS client uses a password.

.. code-block:: yaml

   authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
   auth_certificate_role_queries:
     - source: SUBJECT
       query: CN=([^,\s]+)
   client_encryption_options:
      enabled: True
      certificate: <server cert>
      keyfile: <server key>
      truststore: <shared trust>
      require_client_auth: optional

Create password roles with ``CREATE ROLE ... WITH PASSWORD`` as usual. Certificate logins still need a role whose name the query extracts, with ``LOGIN = true``. Restart the cluster after changing the authenticator.


Additional Resources
--------------------

* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>` 
* :doc:`Authorization </operating-scylla/security/authorization/>` 



