BEGIN;

-- These tables are orphaned: no query reads or writes them any more. The
-- router resolves subscriber capacity through subscriber_entitlements and
-- subscriber_allowance_*, and it never holds a linked subscription account.
-- Drop the dependents before the parents they reference.
DROP TABLE router.model_router_subscription_account_installations;
DROP TABLE router.model_router_subscription_accounts;

DROP TABLE router.subscriber_credit_ledger;
DROP TABLE router.subscriber_credit_reservations;
DROP TABLE router.subscriber_credit_balance;

COMMIT;
