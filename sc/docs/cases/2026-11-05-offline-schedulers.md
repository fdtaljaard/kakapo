# Case: half the CPUs offline on a Standard VM (2026-11-05)

A client's production server (name withheld: this repository is public). Worked through by Francois with an outside advisor; the facts
below were checked against Microsoft Learn on 2026-11-05. Neither the client nor the server is named here.

## What kakapo said

Build 2026-11-05.1936.9802691, Performance card, at 01:40 PM:

> **Pressure right now (PRS01, Critical).** 4 of 8 schedulers are offline. Why: an offline scheduler is a CPU
> SQL Server cannot use: usually the edition's core limit, or a VM with more sockets than the edition takes.
> Do: ask the client's IT to check the VM's sockets and cores against the edition's limit, and the SQL Server
> error log at startup. Evidence: worker threads 66 in use of 576, 0 waiting for a thread; runnable tasks 0 on
> 4 online schedulers, 4 offline; memory grants 0 waiting.

Right to raise it, wrong in three ways: it called a capacity limit "pressure right now" while nothing was
queued; Critical made it a red flag at the top of the database page; and it left the diagnosis to the reader
when the DMVs it could read already hold the answer.

## What the evidence showed

| Read | Value |
|---|---|
| `sys.dm_os_sys_info` | cpu_count 8, scheduler_count 4, socket_count 8, cores_per_socket 1, numa_node_count 1, hyperthread_ratio 1 |
| `sys.dm_os_schedulers` (scheduler_id < 255) | 0 to 3 VISIBLE ONLINE on cpu_id 0 to 3 (8 to 10 tasks each, 0 runnable); 4 to 7 VISIBLE OFFLINE on cpu_id 4 to 7; all on node 0 |
| `sp_configure` | affinity mask 0, affinity64 mask 0, affinity I/O mask 0 |
| Edition | not captured (see below) |

## Root cause

The VM presents its 8 vCPUs as **8 virtual sockets with 1 core each**. SQL Server Standard uses at most
**4 sockets** (or 24 cores, whichever is less), so it starts schedulers on the first 4 sockets only and leaves
the other 4 VISIBLE OFFLINE. The client pays for 8 vCPUs and SQL Server can use 4.

The edition was not read, but the numbers fix it: 4 online of 8 single-core sockets is exactly a 4-socket
limit, which only Standard and Web have (Express would use 1, Enterprise and Developer all 8). Affinity is
ruled out: every mask is 0. To confirm on the server: `SELECT SERVERPROPERTY('Edition'), SERVERPROPERTY('ProductVersion');`
and `EXEC sys.xp_readerrorlog 0, 1, N'socket';` (the startup line below).

## The fix (client IT)

Reconfigure the VM's processor topology, same 8 vCPUs: **1 socket with 8 cores** (or 2 sockets with 4 cores).
In VMware this is "Cores per Socket" in the VM's CPU settings. It needs the VM powered off. No SQL Server
setting changes and the vCPU count stays the same.

## How to verify

After the VM is back up:

```sql
SELECT cpu_count, scheduler_count, socket_count, cores_per_socket FROM sys.dm_os_sys_info;
-- expected: 8, 8, 1, 8
SELECT status, COUNT(*) FROM sys.dm_os_schedulers WHERE scheduler_id < 255 GROUP BY status;
-- expected: VISIBLE ONLINE 8, no VISIBLE OFFLINE
EXEC sys.xp_readerrorlog 0, 1, N'socket';
-- expected: "SQL Server detected 1 sockets with 8 cores per socket ... using 8 logical processors based on SQL Server licensing."
```

## What it teaches kakapo

1. Offline schedulers are a **setup fact, not pressure**: they hold whatever the load, so they belong with the
   instance's configuration, judged on what is lost (half the CPUs), not on a queue that may be empty.
2. **Diagnose, don't guess.** kakapo can read the edition, socket_count, cores_per_socket, cpu_count and the
   affinity, and from them name the cause and the exact VM change. Hand the client's IT the change and the
   verification query, not a homework list.
3. Keep the live context ("0 runnable tasks now") beside it, so nobody reads a capacity limit as current load.

## What changed in kakapo (agreed with Francois, 2026-11-06)

- **New Setup check INS17, "SQL Server uses all the server's CPUs"** (owner client IT, High whatever the load,
  not a red flag). It fires on VISIBLE OFFLINE schedulers when the affinity is AUTO (a MANUAL affinity was a
  choice; INS16 shows the masks) and names the cause from the edition, `socket_count`, `cores_per_socket`,
  `cpu_count` and `virtual_machine_type_desc`, in this order: more sockets than the edition takes on a VM (the
  exact layout to ask for, and the core limit after it), on a physical server (only Enterprise helps), more
  cores than the edition takes, or not known (before 2016 SP2, or an edition without a limit: read the
  startup line). The do-this ends in the verification query for IT. The evidence keeps the runnable tasks now:
  "0 on the 4 online schedulers, so this is lost capacity, not pressure now".
- **PRS01 no longer raises offline schedulers**, so they no longer make a Critical red flag; its evidence still
  counts them and says "(see INS17)".
- kakapo does not read the error log: the DMVs give the cause, and the log line is in the check for IT.
- On that server INS17 reads: "SQL Server uses 4 of the server's 8 CPUs: the VM shows 8 sockets with 1 core each,
  and Standard edition uses at most 4 sockets." Do this: change the VM to 1 socket with 8 cores (or 2 sockets
  with 4 cores each). Tests: `TestINS17` (this shape and its variants), `TestPerformancePRS01`.
- Caveat: `virtual_machine_type_desc` also says HYPERVISOR on a Windows host with Hyper-V on (seen on the
  development laptop), so a physical Standard server with Hyper-V on and more than 4 sockets would get the VM
  wording. Rare enough to leave.

## Facts checked (Microsoft Learn, 2026-11-05)

- **Compute capacity limits, SQL Server 2016 to 2022** (Database Engine, single instance): Enterprise
  (core-based) and Developer: operating system maximum; Enterprise Server + CAL: 20 cores; **Standard: lesser of
  4 sockets or 24 cores**; Web: lesser of 4 sockets or 16 cores; Express (with or without Advanced Services):
  lesser of 1 socket or 4 cores. Same rows in the 2016 and the 2022 editions pages.
  [Editions and features of SQL Server 2022](https://learn.microsoft.com/sql/sql-server/editions-and-components-of-sql-server-2022#scale-limits),
  [SQL Server 2016](https://learn.microsoft.com/previous-versions/sql/sql-server/editions-and-components-of-sql-server-2016#scale-limits).
  SQL Server 2025 changes two of them: Standard becomes 4 sockets or 32 cores, and Web no longer exists
  ([Compute capacity limits by edition](https://learn.microsoft.com/sql/sql-server/compute-capacity-limits-by-edition-of-sql-server), footnote 2).
- **VMs:** the same page: "In a virtualized environment, the compute capacity limit is based on the number of
  logical processors, not cores", because the processor architecture is not visible to the guest. The socket
  limit applies to the sockets the guest OS sees: SQL Server reads the package (socket) layout from Windows'
  `GetLogicalProcessorInformation`, on bare metal and VMs alike
  ([Bob Dorr, How SQL Server determines logical and physical processors](https://learn.microsoft.com/archive/blogs/bobsql/how-it-works-how-sql-server-determines-logical-and-physical-processors)).
  A virtual socket counts as a socket.
- **`VISIBLE OFFLINE`:** "OFFLINE schedulers map to processors that are offline in the affinity mask and are,
  therefore, not being used to process any requests"; VISIBLE schedulers run user requests
  ([sys.dm_os_schedulers](https://learn.microsoft.com/sql/relational-databases/system-dynamic-management-views/sys-dm-os-schedulers-transact-sql)).
  The page names only the affinity mask; the licensing limit shows the same way (this case, and a Microsoft Q&A
  thread with a 16 x 1 VM showing 4 online:
  [SQL Server not using all cores](https://learn.microsoft.com/answers/questions/119509/sql-server-not-using-all-cores)).
- **`sys.dm_os_sys_info`:** `socket_count`, `cores_per_socket` and `numa_node_count` apply to SQL Server 2016 SP2
  and later; `scheduler_count` is the user schedulers configured; `affinity_type_desc` is MANUAL when affinity
  was set for at least one CPU, AUTO otherwise
  ([sys.dm_os_sys_info](https://learn.microsoft.com/sql/relational-databases/system-dynamic-management-views/sys-dm-os-sys-info-transact-sql)).
- **The startup error-log line** (format checked on the local SQL Server 2022 Developer instance and in the
  Dorr post): `SQL Server detected 1 sockets with 10 cores per socket and 20 logical processors per socket, 20 total
  logical processors; using 20 logical processors based on SQL Server licensing. This is an informational message;
  no user action is required.` On that server it would read 8 sockets with 1 core per socket, using 4. Microsoft
  does not document the message itself.
