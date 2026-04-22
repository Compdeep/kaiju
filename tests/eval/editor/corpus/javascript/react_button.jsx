import React from 'react'

export function Button({ variant = 'primary', onClick, children, disabled }) {
  const base = 'inline-flex items-center justify-center rounded px-4 py-2 font-medium transition'
  const variants = {
    primary: 'bg-blue-600 text-white hover:bg-blue-700 disabled:bg-gray-300',
    secondary: 'bg-gray-100 text-gray-900 hover:bg-gray-200 disabled:opacity-50',
    ghost: 'bg-transparent text-gray-700 hover:bg-gray-50',
  }
  return (
    <button
      type="button"
      className={`${base} ${variants[variant] || variants.primary}`}
      onClick={onClick}
      disabled={disabled}
    >
      {children}
    </button>
  )
}
