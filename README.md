# dbairlock

Reviewed, audited data changes for production databases, for engineers and their AI agents.

Instead of running `UPDATE` statements by hand against production, an engineer or an agent submits the change to dbairlock. A teammate reviews it in a web dashboard, seeing exactly which rows it will touch, and dbairlock then executes it. Nobody on the team needs write credentials.

## Status

Not built yet.

## Planned features

- **Agent access over MCP**: agents read the schema and submit changes, but never see row data and cannot approve anything.
- **Web dashboard**: SQL editor with schema autocomplete, review queue, and a before/after view of affected rows.
- **Row changes only**: single-table `INSERT`, `UPDATE` and `DELETE`. No schema migrations.
- **Guarded execution**: a change is rolled back if it would affect a different number of rows than the reviewer approved.
- **Tamper-evident audit log**: every login, submission, review and execution is recorded.
- **Google login**, restricted to your Workspace domain.
- **Postgres and MySQL** targets.
- **Self-hosted**: one Docker image and one volume.
- **Configured in the browser**: no environment variables or config files. A first-run wizard sets up sign-in, and everything else lives in Settings.
- **Write-only secrets**: database passwords can be set and replaced, never read back.

## License

[MIT](LICENSE)
