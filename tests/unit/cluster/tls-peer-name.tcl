# Tests for tls-expected-peer-name on the cluster bus: with the option set,
# a peer must present a CA-signed certificate that also carries one of the
# expected names, so a certificate merely signed by the same (shared) CA is
# not enough to join the bus or forge messages on it.
#
# All nodes use tests/tls/san.crt (SubjectAltName: DNS:redis.local,
# DNS:cluster.local) in both the server and the client role, as required
# when the peer name check is enabled.

if {$::tls} {
    set san_crt [format "%s/tests/tls/san.crt" [pwd]]
    set san_key [format "%s/tests/tls/san.key" [pwd]]

    start_cluster 3 0 [list tags {external:skip cluster tls} overrides [list \
            tls-cert-file $san_crt tls-key-file $san_key \
            tls-client-cert-file $san_crt tls-client-key-file $san_key \
            tls-expected-peer-name cluster.local]] {

        test "Cluster forms and works with tls-expected-peer-name enforced on the bus" {
            # start_cluster already waited for a consistent "ok" state, which
            # requires every bus link (inbound and outbound) to pass the peer
            # name check. Verify the view explicitly.
            for {set i 0} {$i < 3} {incr i} {
                assert_equal 3 [CI $i cluster_known_nodes]
                assert_equal "ok" [CI $i cluster_state]
            }
            assert_equal {PONG} [R 0 PING]
        }

        test "Forged FAIL from a CA-signed cert without the expected name is rejected" {
            set CLUSTERMSG_TYPE_FAIL 3
            set CLUSTERMSG_MIN_LEN 2256
            set CLUSTER_NAMELEN 40

            set target_host [srv 0 host]
            set target_bus_port [expr {[srv 0 port] + 10000}]

            # Impersonate one cluster node (the sender of a bus message is just
            # its node ID in the header) and claim that another node is failing.
            set impersonated_id [R 1 CLUSTER MYID]
            set victim_id [R 2 CLUSTER MYID]
            set sender_port [srv -1 port]
            set sender_cport [expr {$sender_port + 10000}]

            set totlen [expr {$CLUSTERMSG_MIN_LEN + $CLUSTER_NAMELEN}]
            set packet [build_cluster_bus_header $impersonated_id $sender_port \
                $sender_cport $CLUSTERMSG_TYPE_FAIL $totlen]
            append packet [binary format a${CLUSTER_NAMELEN} $victim_id]

            set log_pos [count_log_lines 0]

            # client.crt chains to the same CA as the cluster certificates but
            # carries no SubjectAltName, so it must be rejected during the TLS
            # handshake on the inbound bus connection.
            set fd [::tls::socket \
                -cafile "$::tlsdir/ca.crt" \
                -certfile "$::tlsdir/client.crt" \
                -keyfile "$::tlsdir/client.key" \
                $target_host $target_bus_port]
            fconfigure $fd -translation binary -buffering full
            # Depending on TLS version/timing the failure surfaces on the
            # write or on the subsequent read; either way the packet is never
            # processed.
            catch {
                puts -nonewline $fd $packet
                flush $fd
                read $fd
            }
            catch {close $fd}

            # The target rejected the connection at the TLS layer...
            wait_for_log_messages 0 {"*Error accepting cluster node connection*"} $log_pos 100 100

            # ...and did not act on the forged FAIL.
            assert_equal 0 [check_cluster_node_mark fail 0 $victim_id]
            assert_equal "ok" [CI 0 cluster_state]
            assert_equal {PONG} [R 0 PING]
        }

        test "Bus connection with the expected name in its cert is accepted" {
            # Control check for the previous test: the same connection with
            # the SAN certificate passes the peer name check, proving that
            # the forged FAIL above was rejected by the name check and not
            # by something else.
            set target_host [srv 0 host]
            set target_bus_port [expr {[srv 0 port] + 10000}]

            set log_pos [count_log_lines 0]

            set fd [::tls::socket \
                -cafile "$::tlsdir/ca.crt" \
                -certfile "$::tlsdir/san.crt" \
                -keyfile "$::tlsdir/san.key" \
                $target_host $target_bus_port]
            fconfigure $fd -translation binary -buffering none -blocking 0

            # Drive the handshake to completion from the client side.
            set retries 100
            set hs_done 0
            while {!$hs_done && $retries > 0} {
                if {[catch {set hs_done [::tls::handshake $fd]}]} break
                if {!$hs_done} { after 50 }
                incr retries -1
            }
            assert_equal 1 $hs_done

            # If the server had rejected our certificate it would send a fatal
            # alert and close the connection right away; make sure it did not.
            set got_eof 0
            for {set i 0} {$i < 10} {incr i} {
                catch {read $fd 1}
                if {[eof $fd]} { set got_eof 1 ; break }
                after 100
            }
            catch {close $fd}
            assert_equal 0 $got_eof

            set log [exec tail -n +$log_pos < [srv 0 stdout]]
            assert_no_match "*Error accepting cluster node connection*" $log
        }
    }
}
