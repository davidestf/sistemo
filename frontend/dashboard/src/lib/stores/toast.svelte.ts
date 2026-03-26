type ToastType = 'success' | 'error' | 'info';
let toasts = $state<Array<{ id: number; message: string; type: ToastType }>>([]);
let nextId = 0;

export function addToast(message: string, type: ToastType = 'info') {
  const id = nextId++;
  // Cap visible toasts to prevent flooding
  if (toasts.length >= 5) toasts.shift();
  toasts.push({ id, message, type });
  setTimeout(() => {
    toasts = toasts.filter(t => t.id !== id);
  }, 4000);
}

export function getToasts() {
  return toasts;
}
