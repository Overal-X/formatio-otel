function cartTotal(cart) {
  return cart.items.reduce((total, item) => total + item.cents, 0)
}

module.exports = { cartTotal }
