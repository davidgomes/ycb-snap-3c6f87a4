set testmodule [file normalize tests/modules/internalsupport.so]

start_server {tags {"modules"}} {
    r module load $testmodule

    test "Internal module commands are hidden from ordinary clients" {
        assert_error {*unknown command 'internalsupport.command'*} {r internalsupport.command}
        assert_equal -1 [lsearch -exact [r command list] internalsupport.command]
        assert_equal {} [lindex [r command info internalsupport.command] 0]
        assert_equal {} [r command docs internalsupport.command]
        assert_error {ERR Invalid command specified} {r command getkeys internalsupport.command}
    }

    test "Internal module commands are permitted for unrestricted module calls" {
        assert_equal {OK} [r internalsupport.call]
        assert_error {*unknown command 'internalsupport.command'*} {r internalsupport.call as-user}
    }

    test "Internal module commands are denied from scripts" {
        assert_error {*Unknown Redis command called from script*} {
            r eval {return redis.call('internalsupport.command')} 0
        }
    }

    test "DEBUG marks and unmarks an internal connection" {
        set ordinary_count [r command count]
        assert_equal {OK} [r debug mark-internal-client]
        assert_equal {OK} [r internalsupport.command]
        assert_equal [expr {$ordinary_count + 1}] [r command count]
        assert_not_equal -1 [lsearch -exact [r command list] internalsupport.command]
        assert_not_equal {} [lindex [r command info internalsupport.command] 0]
        assert_not_equal {} [r command docs internalsupport.command]
        assert_error {*no key arguments*} {r command getkeys internalsupport.command}
        assert_match {*flags=*I*} [r client info]
        assert_equal {OK} [r debug mark-internal-client unmark]
        assert_error {*unknown command 'internalsupport.command'*} {r internalsupport.command}
    }

    test "Internal commands are hidden from ordinary MONITOR clients" {
        set monitor [redis_deferring_client]
        $monitor monitor
        $monitor read

        r debug mark-internal-client
        r internalsupport.command
        r ping
        assert_match {*"ping"*} [$monitor read]
        $monitor close
    }

    test "Internal MONITOR clients can observe internal commands" {
        set monitor [redis_deferring_client]
        $monitor debug mark-internal-client
        assert_equal {OK} [$monitor read]
        $monitor monitor
        $monitor read

        r internalsupport.command
        assert_match {*"internalsupport.command"*} [$monitor read]
        $monitor close
    }

    test "Internal commands remain observable in command statistics and slowlog" {
        r config set slowlog-log-slower-than 0
        r slowlog reset
        r internalsupport.command
        assert_match {*cmdstat_internalsupport|command:calls=*} [r info commandstats]
        assert_equal {internalsupport.command} [lindex [lindex [r slowlog get 1] 0] 3]
    }

    test "Non-cluster instances reject internal authentication" {
        r debug mark-internal-client unmark
        assert_error {*only supported in cluster mode*} {r auth {internal connection} wrong-secret}
        assert_equal {} [r internalsupport.get-secret]
    }

    r module unload internalsupport
}

start_server {tags {"modules cluster"} overrides {cluster-enabled {yes}}} {
    r module load $testmodule

    test "AUTH promotes a client to an internal cluster connection" {
        set secret [r internalsupport.get-secret]
        assert_equal 40 [string length $secret]
        assert_error {*WRONGPASS*} {r auth {internal connection} wrong-secret}
        assert_equal {OK} [r auth {internal connection} $secret]
        assert_equal {OK} [r internalsupport.command]
        assert_equal {OK} [r internalsupport.call as-user]
        assert_match {*flags=*I*} [r client info]
    }

    r module unload internalsupport
}
