TLS Support
===========

Getting Started
---------------

### Building

To build with TLS support you'll need OpenSSL development libraries (e.g.
libssl-dev on Debian/Ubuntu).

To build TLS support as Redis built-in:
Run `make BUILD_TLS=yes`.

Or to build TLS as Redis module:
Run `make BUILD_TLS=module`.

Note that sentinel mode does not support TLS module.

### Tests

To run Redis test suite with TLS, you'll need TLS support for TCL (i.e.
`tcl-tls` package on Debian/Ubuntu).

1. Run `./utils/gen-test-certs.sh` to generate a root CA and a server
   certificate.

2. Run `./runtest --tls` or `./runtest-cluster --tls` to run Redis and Redis
   Cluster tests in TLS mode.

3. Run `./runtest --tls-module` or `./runtest-cluster --tls-module` to
   run Redis and Redis cluster tests in TLS mode with Redis module.

### Running manually

To manually run a Redis server with TLS mode (assuming `gen-test-certs.sh` was
invoked so sample certificates/keys are available):

For TLS built-in mode:
    ./src/redis-server --tls-port 6379 --port 0 \
        --tls-cert-file ./tests/tls/redis.crt \
        --tls-key-file ./tests/tls/redis.key \
        --tls-ca-cert-file ./tests/tls/ca.crt

For TLS module mode:
    ./src/redis-server --tls-port 6379 --port 0 \
        --tls-cert-file ./tests/tls/redis.crt \
        --tls-key-file ./tests/tls/redis.key \
        --tls-ca-cert-file ./tests/tls/ca.crt \
        --loadmodule src/redis-tls.so

To connect to this Redis server with `redis-cli`:

    ./src/redis-cli --tls \
        --cert ./tests/tls/redis.crt \
        --key ./tests/tls/redis.key \
        --cacert ./tests/tls/ca.crt

This will disable TCP and enable TLS on port 6379. It's also possible to have
both TCP and TLS available, but you'll need to assign different ports.

To make a Replica connect to the master using TLS, use `--tls-replication yes`,
and to make Redis Cluster use TLS across nodes use `--tls-cluster yes`.

Peer Identity Verification (Trust Model)
----------------------------------------

By default, TLS peer verification only confirms that the peer's certificate
chains to the configured CA (`tls-ca-cert-file`/`tls-ca-cert-dir`) and is not
expired. The trust model is therefore "anyone holding a certificate signed by
the CA". This is appropriate when a dedicated CA is used for a single
cluster/replication group, but under a shared, organizational or public CA it
allows any CA-signed certificate holder to impersonate a master (capturing
replication AUTH credentials via MITM) or a cluster peer (the cluster bus has
no per-message authentication - the sender is just a node ID in the message
header - so an impostor can open a bus connection and forge messages such as
FAIL).

Setting `tls-expected-peer-name` (space-separated list of names, match any)
narrows this trust model for *server-to-server* connections: the peer
certificate must additionally present one of the configured names in its
SubjectAltName extension (Common Name is used as a fallback only when the
certificate carries no DNS SubjectAltName entries). The name is verified by
OpenSSL as part of certificate chain validation. This applies to:

* Outgoing connections: replication links, cluster bus links, and MIGRATE
  (including its blocking connect path).
* Incoming cluster bus connections, where the connecting node's client
  certificate is checked, so impersonation is blocked even when this node
  never dials the attacker.

Ordinary client connections on the data port are not affected: client
identity there remains the responsibility of AUTH/ACL. The expected names
come only from local configuration; the dialed address or anything received
over the wire is never used.

Operationally this means the identity SAN must be present on the certificate
each node uses in **both** its server role (`tls-cert-file`) and its client
role (`tls-client-cert-file`, when configured), since cluster nodes and
replication peers act in both roles.

Peer name verification requires OpenSSL >= 1.0.2 (the `X509_VERIFY_PARAM`
host checking machinery). Building TLS against an older OpenSSL fails with a
clear compile-time error by default. Defining `TLS_NO_PEER_NAME_VERIFICATION`
at compile time (e.g. `make BUILD_TLS=yes REDIS_CFLAGS=-DTLS_NO_PEER_NAME_VERIFICATION`)
opts out: the build succeeds, and if `tls-expected-peer-name` is set a warning
is logged and the name check is skipped (CA verification still applies).

Connections
-----------

All socket operations now go through a connection abstraction layer that hides
I/O and read/write event handling from the caller.

**Multi-threading I/O is not currently supported for TLS**, as a TLS connection
needs to do its own manipulation of AE events which is not thread safe. The
solution is probably to manage independent AE loops for I/O threads and longer
term association of connections with threads. This may potentially improve
overall performance as well.

Sync IO for TLS is currently implemented in a hackish way, i.e. making the
socket blocking and configuring socket-level timeout.  This means the timeout
value may not be so accurate, and there would be a lot of syscall overhead.
However I believe that getting rid of syncio completely in favor of pure async
work is probably a better move than trying to fix that. For replication it would
probably not be so hard. For cluster keys migration it might be more difficult,
but there are probably other good reasons to improve that part anyway.

To-Do List
----------

- [ ] redis-benchmark support. The current implementation is a mix of using
  hiredis for parsing and basic networking (establishing connections), but
  directly manipulating sockets for most actions. This will need to be cleaned
  up for proper TLS support. The best approach is probably to migrate to hiredis
  async mode.
- [ ] redis-cli `--slave` and `--rdb` support.

Multi-port
----------

Consider the implications of allowing TLS to be configured on a separate port,
making Redis listening on multiple ports:

1. Startup banner port notification
2. Proctitle
3. How slaves announce themselves
4. Cluster bus port calculation
