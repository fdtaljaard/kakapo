# Ola Hallengren's SQL Server Maintenance Solution: the index objects

kakapo installs these three scripts in master with the fix `install-ola-index` (docs/phase5u-contract.md).
They are Ola Hallengren's, unchanged, under his MIT licence (`LICENSE`, beside this file).

- Source: https://github.com/olahallengren/sql-server-maintenance-solution, branch `main`,
  downloaded from `https://raw.githubusercontent.com/olahallengren/sql-server-maintenance-solution/main/<file>`
  on 2026-11-11.
- Version: `2026-09-12 15:02:38` (the `--// Version:` line of both procedures).
- SHA-256 (checked by `TestOlaFilesPinned`; a changed byte fails the tests):
  - `CommandLog.sql` 7a508fa7ed562ec5ee09d4f587f7f2a87f7a4eb3c57450602ee99cdeb4e18b31
  - `CommandExecute.sql` 190962fc9802ce8d8f5082a23119567d27f05d2bfc36e1bdf5ce79edea5d8e76
  - `IndexOptimize.sql` ae23d8bd3d866c8c83a6ce27d982b0573e4f96a30e744b3549fa63a9548abed6
  - `LICENSE` 11a9e9cd4b1552936dcae48b1a6b1da0d083767f8b1a813cdf180b38c76ed60d

To move to a newer version: download the four files again, update the version and the hashes here, in
`ola_fix.go` (`OlaVersion`) and in `ola_fix_test.go`, and re-run the live test.
