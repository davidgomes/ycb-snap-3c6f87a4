#
# Copyright (C) 2026-present ScyllaDB
#
# SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
#

"""
Tests for CertificateOrPasswordAuthenticator: a single CQL port accepts both
clients that authenticate with a trusted TLS client certificate and clients
that authenticate with a username and password.
"""

import datetime
import logging
import ssl
from collections.abc import Generator
from pathlib import Path

import pytest
from cassandra import AuthenticationFailed
from cassandra.auth import PlainTextAuthProvider
from cassandra.cluster import Cluster, NoHostAvailable
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

from test.pylib.driver_utils import safe_driver_shutdown
from test.pylib.manager_client import ManagerClient

logger = logging.getLogger(__name__)

PLAIN_PORT = 9042
TLS_PORT = 9142

CERT_ROLE = "cert_user"
PASSWORD_ROLE = "password_user"
PASSWORD = "password_user_secret"

CertificateAuthority = tuple[rsa.RSAPrivateKey, x509.Certificate]
ClientCertificate = tuple[Path, Path]


@pytest.fixture
def cql_clusters() -> Generator[list[Cluster], None, None]:
    clusters: list[Cluster] = []
    yield clusters
    for c in reversed(clusters):
        safe_driver_shutdown(c)


def certificate_builder(subject: x509.Name, issuer: x509.Name, key: rsa.RSAPrivateKey) -> x509.CertificateBuilder:
    now = datetime.datetime.now(datetime.timezone.utc)
    return (x509.CertificateBuilder()
            .subject_name(subject)
            .issuer_name(issuer)
            .public_key(key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(now - datetime.timedelta(days=1))
            .not_valid_after(now + datetime.timedelta(days=30)))


def make_ca(common_name: str) -> CertificateAuthority:
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
    cert = (certificate_builder(name, name, key)
            .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
            .add_extension(x509.KeyUsage(digital_signature=False, content_commitment=False, key_encipherment=False,
                                         data_encipherment=False, key_agreement=False, key_cert_sign=True,
                                         crl_sign=True, encipher_only=False, decipher_only=False), critical=True)
            .sign(key, hashes.SHA256()))
    return key, cert


def make_client_certificate(ca: CertificateAuthority, subject: x509.Name, directory: Path, name: str) -> ClientCertificate:
    ca_key, ca_cert = ca
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    cert = (certificate_builder(subject, ca_cert.subject, key)
            .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
            .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.CLIENT_AUTH]), critical=False)
            .sign(ca_key, hashes.SHA256()))
    cert_path = directory / f"{name}.crt"
    key_path = directory / f"{name}.key"
    cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                           serialization.NoEncryption()))
    return cert_path, key_path


def tls_context(client_certificate: ClientCertificate | None = None) -> ssl.SSLContext:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    # The server presents the test framework's self-signed certificate; only client certificates matter here.
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    if client_certificate:
        cert_path, key_path = client_certificate
        context.load_cert_chain(certfile=cert_path, keyfile=key_path)
    return context


def connect(clusters: list[Cluster], host: str, port: int, ssl_context: ssl.SSLContext | None = None,
            auth_provider: PlainTextAuthProvider | None = None):
    cluster = Cluster([host], port=port, protocol_version=4, ssl_context=ssl_context, auth_provider=auth_provider)
    clusters.append(cluster)
    return cluster.connect()


def connection_errors(clusters: list[Cluster], host: str, port: int, **kwargs) -> list[Exception]:
    with pytest.raises(NoHostAvailable) as exc_info:
        connect(clusters, host, port, **kwargs)
    errors = list(exc_info.value.errors.values())
    assert errors
    return errors


async def test_certificate_and_password_clients_share_port(manager: ManagerClient, tmp_path: Path,
                                                           cql_clusters: list[Cluster]) -> None:
    ca = make_ca("trusted-ca")
    ca_path = tmp_path / "ca.crt"
    ca_path.write_bytes(ca[1].public_bytes(serialization.Encoding.PEM))
    role_cert = make_client_certificate(
        ca, x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, CERT_ROLE)]), tmp_path, "role")
    # Trusted, but its subject has no CN, so no role can be extracted from it.
    no_role_cert = make_client_certificate(
        ca, x509.Name([x509.NameAttribute(NameOID.ORGANIZATION_NAME, "ScyllaDB")]), tmp_path, "no_role")
    untrusted_cert = make_client_certificate(
        make_ca("untrusted-ca"), x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, CERT_ROLE)]), tmp_path, "untrusted")

    server = await manager.server_add(config={
        "authenticator": "CertificateOrPasswordAuthenticator",
        "authorizer": "CassandraAuthorizer",
        "auth_certificate_role_queries": [{"source": "SUBJECT", "query": "CN=([^,]+)"}],
        "native_transport_port": PLAIN_PORT,
        "native_shard_aware_transport_port": 19042,
        "native_transport_port_ssl": TLS_PORT,
        "native_shard_aware_transport_port_ssl": 19142,
        "client_encryption_options": {
            "enabled": True,
            "certificate": "conf/scylla.crt",
            "keyfile": "conf/scylla.key",
            "truststore": ca_path.as_posix(),
            "require_client_auth": "optional",
        },
    })
    host = server.ip_addr
    log = await manager.server_open_log(server.server_id)

    # The test framework's session logged in as the default superuser with a password on the plain port.
    admin = manager.get_cql()
    admin.execute(f"CREATE ROLE {CERT_ROLE} WITH LOGIN = true")
    admin.execute(f"CREATE ROLE {PASSWORD_ROLE} WITH PASSWORD = '{PASSWORD}' AND LOGIN = true")

    password_auth = PlainTextAuthProvider(username=PASSWORD_ROLE, password=PASSWORD)
    wrong_password_auth = PlainTextAuthProvider(username=PASSWORD_ROLE, password="wrong_password")

    def logged_in_over_tls(username: str) -> bool:
        rows = admin.execute(f"SELECT ssl_enabled FROM system.clients WHERE username = '{username}' ALLOW FILTERING")
        return any(row.ssl_enabled for row in rows)

    logger.info("TLS port: a trusted client certificate authenticates without a password")
    connect(cql_clusters, host, TLS_PORT, ssl_context=tls_context(role_cert)).execute("SELECT key FROM system.local")
    assert logged_in_over_tls(CERT_ROLE)

    logger.info("TLS port: a client without a certificate authenticates with a password")
    connect(cql_clusters, host, TLS_PORT, ssl_context=tls_context(),
            auth_provider=password_auth).execute("SELECT key FROM system.local")
    assert logged_in_over_tls(PASSWORD_ROLE)

    logger.info("TLS port: a wrong password fails authentication")
    errors = connection_errors(cql_clusters, host, TLS_PORT, ssl_context=tls_context(), auth_provider=wrong_password_auth)
    assert all(isinstance(e, AuthenticationFailed) for e in errors), errors

    logger.info("Plain port: a password authenticates, a wrong password fails authentication")
    connect(cql_clusters, host, PLAIN_PORT, auth_provider=password_auth).execute("SELECT key FROM system.local")
    errors = connection_errors(cql_clusters, host, PLAIN_PORT, auth_provider=wrong_password_auth)
    assert all(isinstance(e, AuthenticationFailed) for e in errors), errors

    logger.info("TLS port: an untrusted certificate is rejected during the TLS handshake, despite a valid password")
    mark = await log.mark()
    errors = connection_errors(cql_clusters, host, TLS_PORT, ssl_context=tls_context(untrusted_cert),
                               auth_provider=password_auth)
    assert not any(isinstance(e, AuthenticationFailed) for e in errors), errors
    await log.wait_for("Inspecting TLS connection failed", from_mark=mark, timeout=60)

    logger.info("TLS port: a trusted certificate that maps to no role is rejected, despite a valid password")
    errors = connection_errors(cql_clusters, host, TLS_PORT, ssl_context=tls_context(no_role_cert),
                               auth_provider=password_auth)
    assert "does not match any query expression" in str(errors), errors
