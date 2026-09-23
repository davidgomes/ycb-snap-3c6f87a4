#
# Copyright (C) 2026-present ScyllaDB
#
# SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
#

"""
Tests for CertificateOrPasswordAuthenticator: a single CQL port must accept
both clients authenticating with a TLS client certificate and clients
authenticating with username/password.
"""

import datetime
import logging
import ssl
import time
from pathlib import Path

import pytest
from cassandra import AuthenticationFailed
from cassandra.auth import PlainTextAuthProvider
from cassandra.cluster import Cluster, NoHostAvailable
from cassandra.policies import WhiteListRoundRobinPolicy
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

from test.pylib.driver_utils import safe_driver_shutdown
from test.pylib.manager_client import ManagerClient
from test.pylib.util import wait_for_cql_and_get_hosts

logger = logging.getLogger(__name__)

PLAIN_PORT = 9042
SSL_PORT = 9142
CERT_ROLE = "cert_or_password_user"


def _new_key() -> rsa.RSAPrivateKey:
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


def _write_key(key: rsa.RSAPrivateKey, path: Path) -> None:
    path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.TraditionalOpenSSL,
                                       serialization.NoEncryption()))


def _write_cert(cert: x509.Certificate, path: Path) -> None:
    path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))


def _validity(builder: x509.CertificateBuilder) -> x509.CertificateBuilder:
    now = datetime.datetime.now(datetime.timezone.utc)
    return builder.not_valid_before(now - datetime.timedelta(days=1)).not_valid_after(now + datetime.timedelta(days=30))


def make_ca(directory: Path, name: str) -> tuple[rsa.RSAPrivateKey, x509.Certificate, Path]:
    key = _new_key()
    subject = x509.Name([x509.NameAttribute(NameOID.ORGANIZATION_NAME, "ScyllaDB test"),
                         x509.NameAttribute(NameOID.COMMON_NAME, name)])
    cert = _validity(x509.CertificateBuilder()
                     .subject_name(subject)
                     .issuer_name(subject)
                     .public_key(key.public_key())
                     .serial_number(x509.random_serial_number())
                     .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
                     .add_extension(x509.KeyUsage(digital_signature=True, content_commitment=False, key_encipherment=False,
                                                  data_encipherment=False, key_agreement=False, key_cert_sign=True,
                                                  crl_sign=True, encipher_only=False, decipher_only=False), critical=True)
                     ).sign(key, hashes.SHA256())
    path = directory / f"{name}.pem"
    _write_cert(cert, path)
    return key, cert, path


def make_client_cert(directory: Path, name: str, subject: x509.Name,
                     ca_key: rsa.RSAPrivateKey, ca_cert: x509.Certificate) -> tuple[Path, Path]:
    key = _new_key()
    cert = _validity(x509.CertificateBuilder()
                     .subject_name(subject)
                     .issuer_name(ca_cert.subject)
                     .public_key(key.public_key())
                     .serial_number(x509.random_serial_number())
                     .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
                     .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.CLIENT_AUTH]), critical=False)
                     ).sign(ca_key, hashes.SHA256())
    cert_path = directory / f"{name}.crt"
    key_path = directory / f"{name}.key"
    _write_cert(cert, cert_path)
    _write_key(key, key_path)
    return cert_path, key_path


def make_ssl_context(client_cert: tuple[Path, Path] | None = None) -> ssl.SSLContext:
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    if client_cert:
        ctx.load_cert_chain(certfile=str(client_cert[0]), keyfile=str(client_cert[1]))
    return ctx


def connect(host: str, port: int, ssl_context: ssl.SSLContext | None, auth_provider: PlainTextAuthProvider | None) -> Cluster:
    """Connect a dedicated driver session; the caller must shut the returned cluster down."""
    cluster = Cluster(contact_points=[host], port=port, protocol_version=4, ssl_context=ssl_context,
                      auth_provider=auth_provider, load_balancing_policy=WhiteListRoundRobinPolicy([host]),
                      connect_timeout=60, control_connection_timeout=60)
    try:
        session = cluster.connect()
        session.execute("SELECT release_version FROM system.local")
    except BaseException:
        safe_driver_shutdown(cluster)
        raise
    return cluster


def connect_expect_failure(host: str, port: int, ssl_context: ssl.SSLContext | None,
                           auth_provider: PlainTextAuthProvider | None) -> Exception:
    with pytest.raises(NoHostAvailable) as exc_info:
        cluster = connect(host, port, ssl_context, auth_provider)
        safe_driver_shutdown(cluster)
    errors = list(exc_info.value.errors.values())
    assert errors, f"Connection failed without per-host errors: {exc_info.value}"
    logger.info("Connection to %s:%d failed as expected: %s", host, port, errors[0])
    return errors[0]


def password(user: str = "cassandra", pw: str = "cassandra") -> PlainTextAuthProvider:
    return PlainTextAuthProvider(username=user, password=pw)


@pytest.fixture(scope="module")
def certs(tmp_path_factory) -> dict:
    directory = tmp_path_factory.mktemp("cert_or_password_certs")
    ca_key, ca_cert, ca_path = make_ca(directory, "trusted_ca")
    untrusted_ca_key, untrusted_ca_cert, _ = make_ca(directory, "untrusted_ca")
    return {
        "truststore": ca_path,
        "role": make_client_cert(directory, "role", x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, CERT_ROLE)]),
                                 ca_key, ca_cert),
        # Trusted, but its subject has no CN, so no role query matches.
        "no_role": make_client_cert(directory, "no_role",
                                    x509.Name([x509.NameAttribute(NameOID.ORGANIZATION_NAME, "no-role-here")]),
                                    ca_key, ca_cert),
        "untrusted": make_client_cert(directory, "untrusted",
                                      x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, CERT_ROLE)]),
                                      untrusted_ca_key, untrusted_ca_cert),
    }


@pytest.fixture(scope="function")
async def server(manager: ManagerClient, certs: dict):
    config = {
        "authenticator": "com.scylladb.auth.CertificateOrPasswordAuthenticator",
        "authorizer": "AllowAllAuthorizer",
        "auth_certificate_role_queries": [{"source": "SUBJECT", "query": "CN=([^,\\s]+)"}],
        "native_transport_port": PLAIN_PORT,
        "native_shard_aware_transport_port": 19042,
        "native_transport_port_ssl": SSL_PORT,
        "native_shard_aware_transport_port_ssl": 19142,
        "client_encryption_options": {
            "enabled": True,
            "certificate": "conf/scylla.crt",
            "keyfile": "conf/scylla.key",
            "truststore": str(certs["truststore"]),
            "require_client_auth": "optional",
        },
    }
    srv = await manager.server_add(config=config)
    await manager.driver_connect(server=srv, auth_provider=password())
    cql = manager.get_cql()
    await wait_for_cql_and_get_hosts(cql, [srv], time.time() + 60)
    cql.execute(f"CREATE ROLE IF NOT EXISTS {CERT_ROLE} WITH LOGIN = true")
    yield srv


def _usernames_on_ssl_connections(manager: ManagerClient) -> set[str]:
    rows = manager.get_cql().execute("SELECT username, ssl_enabled FROM system.clients WHERE client_type = 'cql' ALLOW FILTERING")
    return {r.username for r in rows if r.ssl_enabled}


async def test_ssl_port_accepts_certificate_and_password_clients(manager: ManagerClient, server, certs: dict) -> None:
    """The TLS port accepts both certificate clients and password clients without a certificate."""
    # A trusted certificate logs in as the role extracted from it, without a SASL exchange.
    cluster = connect(server.ip_addr, SSL_PORT, make_ssl_context(certs["role"]), auth_provider=None)
    safe_driver_shutdown(cluster)

    # A usable certificate decides the identity even if the client also has a password configured.
    cluster = connect(server.ip_addr, SSL_PORT, make_ssl_context(certs["role"]), auth_provider=password())
    try:
        usernames = _usernames_on_ssl_connections(manager)
        assert CERT_ROLE in usernames
        assert "cassandra" not in usernames
    finally:
        safe_driver_shutdown(cluster)

    # Without a certificate, the client falls back to password authentication.
    safe_driver_shutdown(connect(server.ip_addr, SSL_PORT, make_ssl_context(), auth_provider=password()))


async def test_ssl_port_rejects_bad_credentials(server, certs: dict) -> None:
    # A wrong password over TLS fails as an authentication error, not as a TLS/connection error.
    error = connect_expect_failure(server.ip_addr, SSL_PORT, make_ssl_context(), password(pw="wrong"))
    assert isinstance(error, AuthenticationFailed), f"Expected AuthenticationFailed, got {error!r}"

    # An untrusted certificate is rejected during the TLS handshake; valid password credentials must not rescue it.
    error = connect_expect_failure(server.ip_addr, SSL_PORT, make_ssl_context(certs["untrusted"]), password())
    assert not isinstance(error, AuthenticationFailed), \
        f"Untrusted certificate should be rejected during the TLS handshake, got {error!r}"

    # A trusted certificate that maps to no role fails, even though the password credentials are valid.
    error = connect_expect_failure(server.ip_addr, SSL_PORT, make_ssl_context(certs["no_role"]), password())
    assert "does not match any query expression" in str(error), f"Unexpected error: {error!r}"


async def test_plain_port_uses_password_authentication(server) -> None:
    safe_driver_shutdown(connect(server.ip_addr, PLAIN_PORT, None, auth_provider=password()))

    error = connect_expect_failure(server.ip_addr, PLAIN_PORT, None, password(pw="wrong"))
    assert isinstance(error, AuthenticationFailed), f"Expected AuthenticationFailed, got {error!r}"
