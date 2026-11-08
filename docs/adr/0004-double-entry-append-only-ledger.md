# Double-entry, append-only ledger in integer cents

The ledger is double-entry: every Journal Entry's Postings sum to zero, and the database enforces it. Amounts are `BIGINT` cents (USD only for now). The ledger is append-only, so corrections and refunds are reversing Journal Entries, never updates. The Fee is rounded and the Store gets the remainder, so the Postings always balance exactly. There is no stored running balance on `platform_fee_revenue`, because every Order would then lock that one row; balances are computed from Postings.

Example ($100 Order, 5% Fee): Dr `processor_receivable` 10000 / Cr `store_payable:{store_id}` 9500 / Cr `platform_fee_revenue` 500.
