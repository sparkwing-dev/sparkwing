### Security

- **Helm:** Confine launcher node updates and NodeClaim deletes to its Sparkwing capacity, allow only binding-label additions and reservation/quarantine taint changes, and disable API token automount on its ServiceAccount
- **Helm:** Disable token automount on controller and runner ServiceAccounts, including Pods that omit their own automount setting
