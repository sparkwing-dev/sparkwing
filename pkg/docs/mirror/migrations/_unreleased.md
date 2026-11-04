# Migrating to the next release

## Cloud nodes retire after an idle grace

The Cloud launcher retires a used node once it has seen the node unreserved and
empty for 60 seconds, instead of waiting for the node's run to end. It no longer
sends the `retire_run` query on launcher sync, and the controller no longer
accepts it or returns `run_terminal`.

Deploy the controller and launcher from the same release. An older launcher
against a newer controller has its retirement query refused, logs a retirement
warning at most once a minute, and leaves spent nodes to Karpenter
consolidation until it is upgraded. A newer launcher works against an older
controller, because it sends nothing new.

Jobs that become ready more than about a minute after their run's last node
emptied, such as a deploy after an approval, now boot a fresh node. To keep
nodes longer, pass `sparkwing-runner launch --idle-grace <duration>`; it must be
at least `--poll` plus 30 seconds.
