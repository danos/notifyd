# notifyd
Notifyd is a component that listens for configured notifications on
the VCI bus and invokes the script that was configured. This will
allow for feature defined scripts to be called when notifications are
sent on the VCI bus. Ephemeral notifications can be configured by
other scripts using RPCs into the notifier. Notifyd will execute an
appropriate number of scripts in parallel using a queue of
(events,handlers) pairs to ensure that execution order is preserved
with regards to causal order of arrival. This does not mean that
events are totally ordered but that if an event a caused event b to
occur that a will always be processed before b. Order of called
scripts for a given notification is arbitrary and not guaranteed to be
constant.

The following diagram details the parts of the system involved with
notification delivery when using notifyd.
```
              +---------------+
              |               |
              |   Emitter     |
              |               |
              +------+--------+
                     |
                     |
+--------------------v------------+
|                                 |
|             VCI Bus             |
|                                 |
+-------+-------------------------+
        |
        |
    +---v-------+
    |           |
    |  notifyd  +--+  +---------------+
    |           |  |  |               |
    +----+------+  +->+ .../*.notify  |
         |            |               |
         |            +---------------+
    +----v-----+
    |          |
    |  script  |
    |          |
    +----------+

```

## Runtime notification subscriptions
Notifyd has 2 RPCs that allow manipulations of the runtime
notification subscriptions. 'when' adds a new script to be called when
a notification is emitted. 'done' removes the specified script from
the list to be called for a notification.

Notifyd stores the ephemeral notification requests in a cache file
'/run/vci/notifyd.cache'. This file will be restored if the daemon is
restarted during the same boot of the system.

## Static notification subscriptions
Notifyd supports static notification subscription using '.notify'
files located in '/lib/vci/notify/static'. These files are loaded on
every restart of the daemon. The file format is an 'ini' file that
consists of 'When' sections that contain the script to call when the
notification is heard. Below is an example.

```
[When module:name]
script=foo --arg1 --arg2
```

## The script environement
Scripts are called using the UNIX environment and standard interfaces for interaction. The environment will be setup as follows.

| UNIX interface | Function |
| -------------- | -------- |
| stdin          | The rfc7951 encoded data for the notification. |
| stdout         | Debugging output from the script. Logged to notifyd's journal as 'DEBUG'. |
| stderr         | Error output from the script. Logged to notifyd's journal as 'ERR'. |
| NOTIFYD_MODULE | YANG module in which the notification is defined. |
| NOTIFYD_NOTIF  | YANG name for the notification. |
| exit code      | Determins whether the script had an error (0 success; non-0 failure) |

## Limitations
There are some scaling concerns to be aware of when using notifyd.
1. Scripts are called in parallel and will be limited by the number of
processors available to the notifyd process. This is to ensure a 'fork
bomb' can't happen when a notification is emitted.
2. Long executing scripts can hold up other notifications from being
processed in a timely manner. Ensure your scripts are as fast as they
can be when integrating with notifyd.
3. Even quick executing scripts may be too much for notifyd to process
in a realistic timeframe if there are too many registered
scripts. Notifyd should be used sparingly.

## Conclusion
Notifyd is a piece of infrastucture that is meant to be used to enable
small simple components that are ephemeral. It should not be used for
components that can be implemented other ways. Notifyd will ensure
that for simple small components no notifications are missed and their
handlers will be called in a reasonable timeframe. This only remains
true if it is used sparingly.
