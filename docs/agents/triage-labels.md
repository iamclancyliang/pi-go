# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual
label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label
string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.

## Status against the live repo (checked with `gh label list`)

`iamclancyliang/pi-go` currently carries only GitHub's default label set (accessibility, bug,
documentation, duplicate, enhancement, good first issue, help wanted, invalid, question, wontfix).

- `wontfix` — **already exists**, no action needed.
- `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human` — **do not exist yet**. Create
  them before first use of `/triage`, otherwise the skill will create them ad hoc:

```bash
gh label create needs-triage    --repo iamclancyliang/pi-go --color FBCA04 --description "Maintainer needs to evaluate this issue"
gh label create needs-info      --repo iamclancyliang/pi-go --color D876E3 --description "Waiting on reporter for more information"
gh label create ready-for-agent --repo iamclancyliang/pi-go --color 0E8A16 --description "Fully specified, ready for an AFK agent"
gh label create ready-for-human --repo iamclancyliang/pi-go --color 1D76DB --description "Requires human implementation"
```

**One overlap to decide**: GitHub's stock `question` label ("Further information is requested")
means much the same as `needs-info`. Two reasonable choices — create `needs-info` as above and let
`question` mean "someone asked a question", or drop `needs-info` and point the mapping at
`question`. The table above assumes the former; change the right-hand column if you prefer the
latter.

Unlike the upstream `earendil-works/pi` checkout, there is no established label vocabulary here to
respect — this repo is new, so the skill's canonical names can be adopted directly.
