start_server {tags {"tls"}} {
    if {$::tls} {
        package require tls

        test {TLS: Not accepting non-TLS connections on a TLS port} {
            set s [redis [srv 0 host] [srv 0 port]]
            catch {$s PING} e
            set e
        } {*I/O error*}

        test {TLS: Verify tls-auth-clients behaves as expected} {
            set s [redis [srv 0 host] [srv 0 port]]
            ::tls::import [$s channel]
            catch {$s PING} e
            assert_match {*error*} $e

            r CONFIG SET tls-auth-clients no

            set s [redis [srv 0 host] [srv 0 port]]
            ::tls::import [$s channel]
            catch {$s PING} e
            assert_match {PONG} $e

            r CONFIG SET tls-auth-clients optional

            set s [redis [srv 0 host] [srv 0 port]]
            ::tls::import [$s channel]
            catch {$s PING} e
            assert_match {PONG} $e

            r CONFIG SET tls-auth-clients yes

            set s [redis [srv 0 host] [srv 0 port]]
            ::tls::import [$s channel]
            catch {$s PING} e
            assert_match {*error*} $e
        }

        test {TLS: Verify tls-protocols behaves as expected} {
            r CONFIG SET tls-protocols TLSv1.2

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-tls1.2 0}]
            catch {$s PING} e
            assert_match {*I/O error*} $e

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-tls1.2 1}]
            catch {$s PING} e
            assert_match {PONG} $e

            r CONFIG SET tls-protocols ""
        }

        test {TLS: Verify tls-ciphers behaves as expected} {
            r CONFIG SET tls-protocols TLSv1.2
            r CONFIG SET tls-ciphers "DEFAULT:-AES128-SHA256"

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-cipher "-ALL:AES128-SHA256"}]
            catch {$s PING} e
            assert_match {*I/O error*} $e

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-cipher "-ALL:AES256-SHA256"}]
            catch {$s PING} e
            assert_match {PONG} $e

            r CONFIG SET tls-ciphers "DEFAULT"

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-cipher "-ALL:AES128-SHA256"}]
            catch {$s PING} e
            assert_match {PONG} $e

            r CONFIG SET tls-protocols ""
            r CONFIG SET tls-ciphers "DEFAULT"
        }

        test {TLS: Verify tls-prefer-server-ciphers behaves as expected} {
            r CONFIG SET tls-protocols TLSv1.2
            r CONFIG SET tls-ciphers "AES128-SHA256:AES256-SHA256"

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-cipher "AES256-SHA256:AES128-SHA256"}]
            catch {$s PING} e
            assert_match {PONG} $e

            assert_equal "AES256-SHA256" [dict get [::tls::status [$s channel]] cipher]

            r CONFIG SET tls-prefer-server-ciphers yes

            set s [redis [srv 0 host] [srv 0 port] 0 1 {-cipher "AES256-SHA256:AES128-SHA256"}]
            catch {$s PING} e
            assert_match {PONG} $e

            assert_equal "AES128-SHA256" [dict get [::tls::status [$s channel]] cipher]

            r CONFIG SET tls-protocols ""
            r CONFIG SET tls-ciphers "DEFAULT"
        }

        test {TLS: Verify tls-cert-file is also used as a client cert if none specified} {
            set master [srv 0 client]
            set master_host [srv 0 host]
            set master_port [srv 0 port]

            # Use a non-restricted client/server cert for the replica
            set redis_crt [format "%s/tests/tls/redis.crt" [pwd]]
            set redis_key [format "%s/tests/tls/redis.key" [pwd]]

            start_server [list overrides [list tls-cert-file $redis_crt tls-key-file $redis_key] \
                               omit [list tls-client-cert-file tls-client-key-file]] {
                set replica [srv 0 client]
                $replica replicaof $master_host $master_port
                wait_for_condition 30 100 {
                    [string match {*master_link_status:up*} [$replica info replication]]
                } else {
                    fail "Can't authenticate to master using just tls-cert-file!"
                }
            }
        }

        test {TLS: switch between tcp and tls ports} {
            set srv_port [srv 0 port]

            # TLS
            set rd [redis [srv 0 host] $srv_port 0 1]
            $rd PING

            # TCP
            $rd CONFIG SET tls-port 0
            $rd CONFIG SET port $srv_port
            $rd close

            set rd [redis [srv 0 host] $srv_port 0 0]
            $rd PING

            # TLS
            $rd CONFIG SET port 0
            $rd CONFIG SET tls-port $srv_port
            $rd close

            set rd [redis [srv 0 host] $srv_port 0 1]
            $rd PING
            $rd close
        }

        test {TLS: Working with an encrypted keyfile} {
            # Create an encrypted version
            set keyfile [lindex [r config get tls-key-file] 1]
            set keyfile_encrypted "$keyfile.encrypted"
            exec -ignorestderr openssl rsa -in $keyfile -out $keyfile_encrypted -aes256 -passout pass:1234 2>/dev/null

            # Using it without a password fails
            catch {r config set tls-key-file $keyfile_encrypted} e
            assert_match {*Unable to update TLS*} $e

            # Now use a password
            r config set tls-key-file-pass 1234
            r config set tls-key-file $keyfile_encrypted
        }

	    test {TLS: Auto-authenticate using tls-auth-clients-user (CN)} {
	        # Create a user matching the CN in the client certificate (CN=Client-only)
	        r ACL SETUSER {Client-only} on >clientpass allcommands allkeys

	        # Map the client certificate CN to the ACL user name.
	        r CONFIG SET tls-auth-clients-user CN

	        # Connect over TLS using the test client certificate (CN=Client-only)
	        set s [redis [srv 0 host] [srv 0 port] 0 1]
	        catch {$s PING} e
	        assert_match {PONG} $e
	        assert_equal "Client-only" [$s ACL WHOAMI]
	    }

	    foreach user_type {"non-existent" "disabled"} {
	        test "TLS: $user_type user cannot auto-authenticate via certificate" {
	            if {$user_type eq "non-existent"} {
	                # Ensure the Client-only user does not exist so auto-auth will fail
	                catch {r ACL DELUSER {Client-only}}
	            } else {
	                r ACL SETUSER {Client-only} on >clientpass allcommands allkeys
	                r ACL SETUSER {Client-only} off  ;# Disable the user
	            }
	            r ACL LOG RESET
	            r CONFIG SET tls-auth-clients-user CN

	            # Capture the current value of acl_access_denied_tls_cert from INFO stats
	            set info_before [r INFO stats]
	            regexp {acl_access_denied_tls_cert:(\d+)} $info_before -> before

	            # Connect over TLS using the test client certificate (CN=Client-only)
	            # Since there is no matching ACL user or user is disabled, auto-auth should fail
	            # and the connection should remain authenticated as the default user
	            set s [redis [srv 0 host] [srv 0 port] 0 1]
	            assert_equal "default" [$s ACL WHOAMI]

	            # The ACL LOG should contain a single entry with reason "tls-cert"
	            # and username "Client-only"
	            set log [r ACL LOG]
	            assert_equal 1 [llength $log]
	            set entry [lindex $log 0]
	            assert_equal "tls-cert" [dict get $entry reason]
	            assert_equal "Client-only" [dict get $entry username]

	            # INFO stats should report that acl_access_denied_tls_cert increased by 1
	            set info_after [r INFO stats]
	            regexp {acl_access_denied_tls_cert:(\d+)} $info_after -> after
	            assert {$after == $before + 1}

	            # Verify fallback to password auth works after cert auth fails
	            r ACL SETUSER testuser on >testpass +@all ~*
	            $s AUTH testuser testpass
	            assert_equal "testuser" [$s ACL WHOAMI]
	            assert_equal "PONG" [$s PING]

	            # Clean up
	            r ACL DELUSER testuser
	            catch {r ACL DELUSER {Client-only}}
	        }
	    }

        test {TLS: tls-expected-peer-name rejects invalid values at config time} {
            # Whitespace-only values are rejected
            assert_error "*at least one name*" {r CONFIG SET tls-expected-peer-name " "}
            assert_error "*at least one name*" {r CONFIG SET tls-expected-peer-name "   "}

            # Non-space whitespace inside the value is rejected
            assert_error "*whitespace*" {r CONFIG SET tls-expected-peer-name "foo\tbar"}
            assert_error "*whitespace*" {r CONFIG SET tls-expected-peer-name "foo\nbar"}
            assert_error "*whitespace*" {r CONFIG SET tls-expected-peer-name "redis.local\t"}

            # Valid single name and multi-name lists are accepted
            r CONFIG SET tls-expected-peer-name "redis.local"
            assert_equal {redis.local} [lindex [r CONFIG GET tls-expected-peer-name] 1]
            r CONFIG SET tls-expected-peer-name "a.example b.example"
            assert_equal {a.example b.example} [lindex [r CONFIG GET tls-expected-peer-name] 1]

            # An empty value clears the setting
            r CONFIG SET tls-expected-peer-name ""
            assert_equal {} [lindex [r CONFIG GET tls-expected-peer-name] 1]
        }

        test {TLS: tls-expected-peer-name enforced on replication} {
            # Master presents the certificate carrying the shared SANs
            # (DNS:redis.local, DNS:cluster.local).
            set san_crt [format "%s/tests/tls/san.crt" [pwd]]
            set san_key [format "%s/tests/tls/san.key" [pwd]]

            start_server [list overrides [list tls-cert-file $san_crt tls-key-file $san_key]] {
                set master [srv 0 client]
                set master_host [srv 0 host]
                set master_port [srv 0 port]

                start_server {} {
                    set replica [srv 0 client]

                    # A mismatching expected name must keep the link down: the
                    # master cert is CA-signed but does not carry this name.
                    $replica CONFIG SET tls-expected-peer-name wrong.name
                    $replica replicaof $master_host $master_port
                    wait_for_log_messages 0 {"*SYNC*certificate verify failed*"} 0 100 100
                    assert_match {*master_link_status:down*} [$replica INFO replication]

                    # Runtime CONFIG SET of a multi-name list that includes one
                    # of the master's SANs lets replication establish (any name
                    # in the list may match).
                    $replica CONFIG SET tls-expected-peer-name "foo.example redis.local"
                    wait_for_sync $replica
                    assert_match {*master_link_status:up*} [$replica INFO replication]

                    # A single exact SAN name also matches on a fresh link.
                    $replica replicaof no one
                    $replica CONFIG SET tls-expected-peer-name redis.local
                    $replica replicaof $master_host $master_port
                    wait_for_sync $replica

                    # Clearing the option preserves plain CA-only verification.
                    $replica replicaof no one
                    $replica CONFIG SET tls-expected-peer-name ""
                    $replica replicaof $master_host $master_port
                    wait_for_sync $replica
                }
            }
        }

        test {TLS: tls-expected-peer-name falls back to CN when the cert has no SAN} {
            # The default test master cert (server.crt) has CN=Server-only and
            # no SubjectAltName entries, so the CN is used for matching.
            set master_host [srv 0 host]
            set master_port [srv 0 port]

            start_server {} {
                set replica [srv 0 client]

                $replica CONFIG SET tls-expected-peer-name wrong.name
                $replica replicaof $master_host $master_port
                wait_for_log_messages 0 {"*SYNC*certificate verify failed*"} 0 100 100
                assert_match {*master_link_status:down*} [$replica INFO replication]

                $replica CONFIG SET tls-expected-peer-name Server-only
                wait_for_sync $replica
            }
        }

        test {TLS: tls-expected-peer-name enforced on MIGRATE} {
            # MIGRATE uses the cluster connection type, which is TLS here since
            # the test suite runs all servers with tls-cluster yes. The target
            # presents the certificate carrying the shared SANs.
            set san_crt [format "%s/tests/tls/san.crt" [pwd]]
            set san_key [format "%s/tests/tls/san.key" [pwd]]

            start_server [list overrides [list tls-cert-file $san_crt tls-key-file $san_key]] {
                set target_host [srv 0 host]
                set target_port [srv 0 port]

                start_server {} {
                    r SET migkey somevalue

                    # With a mismatching expected name the blocking connect to
                    # the target must fail, so the key stays local.
                    r CONFIG SET tls-expected-peer-name wrong.name
                    catch {r MIGRATE $target_host $target_port migkey 0 1000} e
                    assert_match {*IOERR*} $e
                    assert_equal {somevalue} [r GET migkey]

                    # With an expected name matching one of the target's SANs
                    # the migration succeeds.
                    r CONFIG SET tls-expected-peer-name cluster.local
                    assert_equal {OK} [r MIGRATE $target_host $target_port migkey 0 1000]
                    assert_equal {} [r GET migkey]
                }
            }
        }

        test {TLS: tls-expected-peer-name does not affect ordinary clients} {
            # Data port client certificates are not name-checked; AUTH/ACL
            # remain responsible for client identity.
            r CONFIG SET tls-expected-peer-name some.other.name

            # The test client certificate (client.crt) does not carry that
            # name, yet a new data port connection must still be accepted.
            set s [redis [srv 0 host] [srv 0 port] 0 1]
            assert_equal {PONG} [$s PING]
            $s close

            r CONFIG SET tls-expected-peer-name ""
        }
    }
}
