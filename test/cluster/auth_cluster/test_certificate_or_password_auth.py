#
# Copyright (C) 2026-present ScyllaDB
#
# SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
#

import ssl
import subprocess
import time
from pathlib import Path

import pytest
from cassandra import AuthenticationFailed
from cassandra.auth import PlainTextAuthProvider
from cassandra.cluster import Cluster, NoHostAvailable

from test.pylib.manager_client import ManagerClient
from test.pylib.util import wait_for


def create_self_signed_certificate(directory: Path, name: str, common_name: str) -> tuple[Path, Path]:
    certificate = directory / f"{name}.crt"
    key = directory / f"{name}.key"
    subprocess.run([
        "openssl", "req", "-new", "-x509", "-nodes", "-sha256", "-days", "2",
        "-subj", f"/CN={common_name}",
        "-keyout", str(key), "-out", str(certificate),
    ], check=True, capture_output=True, text=True)
    return certificate, key


def make_ssl_context(server_certificate: Path, client_certificate: Path | None = None,
                     client_key: Path | None = None) -> ssl.SSLContext:
    context = ssl.create_default_context(ssl.Purpose.SERVER_AUTH, cafile=server_certificate)
    context.check_hostname = False
    if client_certificate:
        assert client_key
        context.load_cert_chain(client_certificate, client_key)
    return context


def connect(host: str, port: int, *, ssl_context: ssl.SSLContext | None = None,
            auth_provider: PlainTextAuthProvider | None = None):
    cluster = Cluster([host], port=port, ssl_context=ssl_context, auth_provider=auth_provider,
                      protocol_version=4, connect_timeout=10)
    try:
        return cluster, cluster.connect()
    except Exception:
        cluster.shutdown()
        raise


async def test_certificate_or_password_authenticator(manager: ManagerClient, tmp_path: Path) -> None:
    resources = Path(__file__).parents[2] / "pylib" / "resources"
    server_certificate = resources / "scylla.crt"
    server_key = resources / "scylla.key"
    roleless_certificate, roleless_key = create_self_signed_certificate(tmp_path, "roleless", "no-matching-role")
    untrusted_certificate, untrusted_key = create_self_signed_certificate(tmp_path, "untrusted", "example.com")

    truststore = tmp_path / "trusted-clients.pem"
    truststore.write_bytes(server_certificate.read_bytes() + b"\n" + roleless_certificate.read_bytes())

    config = {
        "authenticator": "CertificateOrPasswordAuthenticator",
        "authorizer": "CassandraAuthorizer",
        "auth_superuser_name": "example.com",
        "auth_certificate_role_queries": [{"source": "SUBJECT", "query": r"CN=(example\.com)(?:,|$)"}],
        "client_encryption_options": {
            "enabled": True,
            "certificate": str(server_certificate),
            "keyfile": str(server_key),
            "truststore": str(truststore),
            "require_client_auth": "optional",
        },
    }
    server = await manager.server_add(config=config, connect_driver=False)
    password_auth = PlainTextAuthProvider(username="example.com", password="cassandra")

    async def plain_cql_is_ready():
        try:
            await manager.driver_connect(server=server, auth_provider=password_auth)
        except NoHostAvailable:
            return None
        return True

    await wait_for(plain_cql_is_ready, time.time() + 60)
    manager.get_cql().execute("SELECT release_version FROM system.local")

    certificate_context = make_ssl_context(server_certificate, server_certificate, server_key)
    certificate_cluster, certificate_session = connect(server.ip_addr, 9142, ssl_context=certificate_context)
    try:
        certificate_session.execute("SELECT release_version FROM system.local")
    finally:
        certificate_cluster.shutdown()

    password_context = make_ssl_context(server_certificate)
    password_cluster, password_session = connect(
            server.ip_addr, 9142, ssl_context=password_context, auth_provider=password_auth)
    try:
        password_session.execute("SELECT release_version FROM system.local")
    finally:
        password_cluster.shutdown()

    wrong_password = PlainTextAuthProvider(username="example.com", password="wrong")
    with pytest.raises(NoHostAvailable) as wrong_password_error:
        connect(server.ip_addr, 9142, ssl_context=make_ssl_context(server_certificate),
                auth_provider=wrong_password)
    assert any(isinstance(error, AuthenticationFailed)
               for error in wrong_password_error.value.errors.values())

    with pytest.raises(NoHostAvailable):
        connect(server.ip_addr, 9142,
                ssl_context=make_ssl_context(server_certificate, untrusted_certificate, untrusted_key),
                auth_provider=password_auth)

    with pytest.raises(NoHostAvailable) as role_error:
        connect(server.ip_addr, 9142,
                ssl_context=make_ssl_context(server_certificate, roleless_certificate, roleless_key),
                auth_provider=password_auth)
    assert any(isinstance(error, AuthenticationFailed) for error in role_error.value.errors.values())
