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

Certificate and password authentication on one port
---------------------------------------------------

Use ``CertificateOrPasswordAuthenticator`` while migrating clients from passwords to
client certificates. The qualified authenticator name is
``com.scylladb.auth.CertificateOrPasswordAuthenticator``.

Configure TLS to request a client certificate without requiring one:

.. code-block:: yaml

   authenticator: com.scylladb.auth.CertificateOrPasswordAuthenticator
   auth_certificate_role_queries:
      - source: SUBJECT
        query: CN=([^,\s]+)

   client_encryption_options:
      enabled: true
      certificate: <server cert>
      keyfile: <server key>
      truststore: <trusted client certificate authorities>
      require_client_auth: optional

``require_client_auth: optional`` makes the TLS server request a client certificate.
Clients without one can complete the TLS handshake and authenticate with a user name
and password. Do not set ``require_client_auth: true`` during this migration: that
mode rejects clients without certificates during the TLS handshake, before password
authentication can start. Setting it to ``false`` does not request client certificates.

When a client presents a certificate, ScyllaDB validates it during the TLS handshake
and uses ``auth_certificate_role_queries`` to select the role. It does not offer
password authentication if role extraction or certificate authentication fails.
An untrusted certificate fails the TLS handshake. Connections to an enabled plain CQL
port do not have a certificate and use password authentication.


Additional Resources
--------------------

* :doc:`Enable Authentication </operating-scylla/security/authentication/>`
* :doc:`Enable Authorization </operating-scylla/security/enable-authorization/>` 
* :doc:`Authorization </operating-scylla/security/authorization/>` 



