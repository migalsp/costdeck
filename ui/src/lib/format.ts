// formatMoney renders an amount in the given currency, with cents only for small amounts.
export const formatMoney = (amount: number, currency = 'USD') => {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumFractionDigits: amount < 10 ? 2 : 0 }).format(amount)
  } catch {
    return `${amount.toFixed(2)} ${currency}`
  }
}
