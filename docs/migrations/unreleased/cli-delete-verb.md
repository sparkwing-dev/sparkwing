## delete is the one removal verb

**Before:** four command groups removed things with `remove` while `secrets`
and `subscriptions` used `delete`.

**After:** every group uses `delete`, with the same flags as before:

| Before | After |
|---|---|
| `sparkwing pipeline sparks remove --spark NAME` | `sparkwing pipeline sparks delete --spark NAME` |
| `sparkwing profiles remove --profile NAME` | `sparkwing profiles delete --profile NAME` |
| `sparkwing repos remove` | `sparkwing repos delete` |
| `sparkwing runners remove --profile NAME` | `sparkwing runners delete --profile NAME` |

An old spelling is refused with a usage error that names the `delete` command,
and nothing runs. Update scripts that call the old spellings.
