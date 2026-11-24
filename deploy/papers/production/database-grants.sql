-- Review-only grant contract, execute only against the intended database
-- after the Papers migration and dedicated LOGIN roles have been created
-- without inherited role memberships or broad application privileges.
-- Passwords must be supplied through private operator provisioning, never
-- embedded in this file. The daemon independently verifies column privileges.
BEGIN;
GRANT CONNECT ON DATABASE atoman_prod TO atoman_papers_worker, atoman_papers_launcher_ro;
GRANT USAGE ON SCHEMA public TO atoman_papers_worker, atoman_papers_launcher_ro;
GRANT SELECT ON TABLE public.paper_compile_jobs, public.paper_compile_slots,
 public.paper_projects, public.paper_access_grants
 TO atoman_papers_worker, atoman_papers_launcher_ro;
GRANT UPDATE (status, attempts, started_at, finished_at, sandbox_id, worker_id,
 lease_token, lease_epoch, updated_at, cancel_requested, failure_code, pdf_digest, pdf_size)
 ON public.paper_compile_jobs TO atoman_papers_worker;
GRANT UPDATE (job_id, worker_id, lease_token, epoch, lease_expires_at,
 updated_at, blocked, block_reason)
 ON public.paper_compile_slots TO atoman_papers_worker;
ALTER ROLE atoman_papers_launcher_ro IN DATABASE atoman_prod
 SET default_transaction_read_only = on;
COMMIT;
