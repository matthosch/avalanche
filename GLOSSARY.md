# Avalanche

A multi-store flash-sale commerce platform: Stores run Flash Sales, Shoppers race to claim limited stock, and every completed Order is booked into a double-entry ledger.

## Commerce

**Platform**:
The operator that hosts many Stores and takes a fee on every Order.
_Avoid_: marketplace, site

**Store**:
A storefront on the Platform that runs Flash Sales and is paid out for its Orders.
_Avoid_: shop, merchant (when meaning the storefront)

**Merchant**:
The person or organization operating a Store. One Merchant operates exactly one Store.
_Avoid_: seller, vendor, account

**Shopper**:
A person buying on the Platform, identified by a Shopper ID across all Stores.
_Avoid_: user, customer, buyer, account

**Flash Sale**:
A time-boxed event in which one Store sells a fixed number of units of one product at one price.
_Avoid_: sale (on its own), drop, campaign

**Close-out**:
The point at which an ended Flash Sale has no open Holds left and its final counts become the permanent record.
_Avoid_: settlement, finalization

**Hold**:
A Shopper's temporary claim on one unit of a Flash Sale, which either becomes an Order or is released.
_Avoid_: reservation, cart, lock

**Order**:
The record of a Shopper's completed purchase, created when a Hold is sold.
_Avoid_: purchase, sale, transaction

**OrderCompleted**:
The fact, published by commerce, that an Order was paid for.
_Avoid_: SaleCompleted

## Ledger

**Ledger Account**:
A named bucket of money in the ledger, such as a Store's payable balance or the Platform's fee revenue.
_Avoid_: account (on its own), wallet, balance

**Journal Entry**:
One balanced, immutable record of money moving between Ledger Accounts; its Postings sum to zero.
_Avoid_: transaction, entry (on its own)

**Posting**:
A single debit or credit to one Ledger Account within a Journal Entry.
_Avoid_: line, entry line, split

**Fee**:
The Platform's cut of an Order, set by a rate captured at the moment of the Order.
_Avoid_: commission, take rate
