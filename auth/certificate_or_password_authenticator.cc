/*
 * Copyright (C) 2026-present ScyllaDB
 *
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#include "auth/certificate_or_password_authenticator.hh"
#include "auth/authenticated_user.hh"

namespace auth {

constexpr std::string_view certificate_or_password_authenticator_name("com.scylladb.auth.CertificateOrPasswordAuthenticator");

certificate_or_password_authenticator::certificate_or_password_authenticator(cql3::query_processor& qp, ::service::raft_group0_client& g0, ::service::migration_manager& mm, cache& cache, const config& cfg)
    : password_authenticator(qp, g0, mm, cache, cfg)
    , _certificate_authenticator(qp, g0, mm, cache, cfg)
{}

certificate_or_password_authenticator::~certificate_or_password_authenticator() = default;

future<> certificate_or_password_authenticator::start() {
    co_await _certificate_authenticator.start();
    co_await password_authenticator::start();
}

future<> certificate_or_password_authenticator::stop() {
    co_await password_authenticator::stop();
    co_await _certificate_authenticator.stop();
}

std::string_view certificate_or_password_authenticator::qualified_java_name() const {
    return certificate_or_password_authenticator_name;
}

future<std::optional<authenticated_user>> certificate_or_password_authenticator::authenticate(session_dn_func f) const {
    if (!f) {
        co_return std::nullopt;
    }
    auto cert = co_await f();
    if (!cert) {
        // No client certificate: the client proceeds with SASL username/password authentication.
        co_return std::nullopt;
    }
    // The certificate was already verified against the trust store during the TLS handshake. Failures
    // from here on (e.g. no role query matches) must propagate, so a client that presented a
    // certificate can never be admitted by falling back to a password.
    co_return co_await _certificate_authenticator.authenticate([cert = std::move(*cert)] {
        return make_ready_future<std::optional<certificate_info>>(cert);
    });
}

}
