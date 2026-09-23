/*
 * Copyright (C) 2026-present ScyllaDB
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#include "auth/certificate_or_password_authenticator.hh"

#include "auth/authenticated_user.hh"
#include "auth/certificate_authenticator.hh"
#include "auth/password_authenticator.hh"
#include "utils/log.hh"

namespace auth {

static logging::logger logger("certificate_or_password_authenticator");

certificate_or_password_authenticator::certificate_or_password_authenticator(cql3::query_processor& qp,
        ::service::raft_group0_client& g0,
        ::service::migration_manager& mm,
        cache& cache,
        const config& cfg)
    : _certificate(std::make_unique<certificate_authenticator>(qp, g0, mm, cache, cfg))
    , _password(std::make_unique<password_authenticator>(qp, g0, mm, cache, cfg)) {
}

certificate_or_password_authenticator::~certificate_or_password_authenticator() = default;

future<> certificate_or_password_authenticator::start() {
    co_await _certificate->start();
    co_await _password->start();
}

future<> certificate_or_password_authenticator::stop() {
    co_await _password->stop();
    co_await _certificate->stop();
}

std::string_view certificate_or_password_authenticator::qualified_java_name() const {
    return certificate_or_password_authenticator_name;
}

bool certificate_or_password_authenticator::require_authentication() const {
    return true;
}

authentication_option_set certificate_or_password_authenticator::supported_options() const {
    return _password->supported_options();
}

authentication_option_set certificate_or_password_authenticator::alterable_options() const {
    return _password->alterable_options();
}

future<authenticated_user> certificate_or_password_authenticator::authenticate(const credentials_map& credentials) const {
    return _password->authenticate(credentials);
}

future<std::optional<authenticated_user>> certificate_or_password_authenticator::authenticate(session_dn_func f) const {
    // No transport identity at all (or a non-TLS connection that cannot
    // supply one) is the password path. CertificateAuthenticator itself
    // returns nullopt only when the callback is empty; a missing certificate
    // is an error there, which would reject password-only clients.
    if (!f) {
        co_return std::nullopt;
    }

    auto dninfo = co_await f();
    if (!dninfo) {
        logger.debug("No client certificate; using password authentication");
        co_return std::nullopt;
    }

    // TLS already validated this certificate. Hold onto that DN so role
    // extraction cannot observe a later "no certificate" result and so a
    // mismatch cannot be retried as a password.
    logger.debug("Client certificate present; authenticating from certificate");
    certificate_info presented = std::move(*dninfo);
    co_return co_await _certificate->authenticate([presented = std::move(presented)] {
        return make_ready_future<std::optional<certificate_info>>(presented);
    });
}

future<> certificate_or_password_authenticator::create(std::string_view role_name, const authentication_options& options, ::service::group0_batch& mc) {
    return _password->create(role_name, options, mc);
}

future<> certificate_or_password_authenticator::alter(std::string_view role_name, const authentication_options& options, ::service::group0_batch& mc) {
    return _password->alter(role_name, options, mc);
}

future<> certificate_or_password_authenticator::drop(std::string_view role_name, ::service::group0_batch& mc) {
    return _password->drop(role_name, mc);
}

future<custom_options> certificate_or_password_authenticator::query_custom_options(std::string_view role_name) const {
    return _password->query_custom_options(role_name);
}

bool certificate_or_password_authenticator::uses_password_hashes() const {
    return _password->uses_password_hashes();
}

future<std::optional<sstring>> certificate_or_password_authenticator::get_password_hash(std::string_view role_name) const {
    return _password->get_password_hash(role_name);
}

const resource_set& certificate_or_password_authenticator::protected_resources() const {
    return _password->protected_resources();
}

::shared_ptr<sasl_challenge> certificate_or_password_authenticator::new_sasl_challenge() const {
    return _password->new_sasl_challenge();
}

future<> certificate_or_password_authenticator::ensure_superuser_is_created() const {
    return _password->ensure_superuser_is_created();
}

}
