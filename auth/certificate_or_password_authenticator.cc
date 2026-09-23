/*
 * Copyright (C) 2026-present ScyllaDB
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#include "auth/certificate_or_password_authenticator.hh"

#include "auth/certificate_authenticator.hh"
#include "auth/password_authenticator.hh"

#include <seastar/core/coroutine.hh>

#include "exceptions/exceptions.hh"
#include "utils/error_injection.hh"
#include "utils/log.hh"

namespace auth {

static logging::logger logger("certificate_or_password_authenticator");

static constexpr std::string_view certificate_or_password_authenticator_name =
        "com.scylladb.auth.CertificateOrPasswordAuthenticator";

certificate_or_password_authenticator::certificate_or_password_authenticator(
        cql3::query_processor& qp,
        ::service::raft_group0_client& g0,
        ::service::migration_manager& mm,
        cache& cache,
        const config& cfg)
    : _certificate(std::make_unique<certificate_authenticator>(qp, g0, mm, cache, cfg))
    , _password(std::make_unique<password_authenticator>(qp, g0, mm, cache, cfg))
{}

future<> certificate_or_password_authenticator::start() {
    co_await _password->start();
    co_await _certificate->start();
}

future<> certificate_or_password_authenticator::stop() {
    co_await _certificate->stop();
    co_await _password->stop();
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
    // Same test hook as CertificateAuthenticator: return the injected role
    // without consulting the socket.
    if (utils::get_local_injector().inject_parameter("transport_early_auth_bypass")) {
        co_return co_await _certificate->authenticate(std::move(f));
    }
    if (!f) {
        co_return std::nullopt;
    }

    auto dninfo = co_await f();
    if (!dninfo) {
        // Optional/request client auth, or a plain non-TLS connection.
        logger.debug("No client certificate; using password authentication");
        co_return std::nullopt;
    }

    // The certificate was accepted by TLS. Role-query failure must not fall
    // through to password SASL. Untrusted certificates never reach this point:
    // TLS verification closes the handshake first.
    logger.debug("Client certificate present; using certificate authentication");
    auto user = co_await _certificate->authenticate([info = std::move(*dninfo)]() {
        return make_ready_future<std::optional<certificate_info>>(info);
    });
    // A presented certificate never falls through to password SASL, including
    // when role extraction produces no user.
    if (!user) {
        throw exceptions::authentication_exception("Certificate authentication failed");
    }
    co_return user;
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
