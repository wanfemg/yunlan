// Local generation history, matching new_api_tools' browser-only storage.
export type RedemptionHistoryRecord = {
  id: string
  timestamp: string
  name: string
  count: number
  keys: string[]
  quota_mode: 'fixed' | 'random'
  expire_mode: 'never' | 'days' | 'date'
  mode: string
}

function openRedemptionHistory(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('siteadmin-redemptions', 1)
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains('generated_batches')) {
        request.result.createObjectStore('generated_batches', { keyPath: 'id' })
      }
    }
    request.onerror = () => reject(request.error ?? new Error('无法打开本地生成记录'))
    request.onblocked = () => reject(new Error('本地生成记录正在被其他页面更新'))
    request.onsuccess = () => {
      request.result.onversionchange = () => request.result.close()
      resolve(request.result)
    }
  })
}

export async function readRedemptionHistory(): Promise<RedemptionHistoryRecord[]> {
  const database = await openRedemptionHistory()
  return new Promise((resolve, reject) => {
    const transaction = database.transaction('generated_batches', 'readonly')
    const request = transaction.objectStore('generated_batches').getAll()
    transaction.oncomplete = () => {
      database.close()
      const records = request.result as RedemptionHistoryRecord[]
      resolve(records.filter(record => record && typeof record.id === 'string' &&
        typeof record.timestamp === 'string' && Number.isFinite(Date.parse(record.timestamp)) &&
        typeof record.name === 'string' && Array.isArray(record.keys) &&
        record.keys.every(key => typeof key === 'string'))
        .sort((a, b) => b.timestamp.localeCompare(a.timestamp)).slice(0, 100))
    }
    transaction.onerror = () => { database.close(); reject(transaction.error ?? new Error('读取本地生成记录失败')) }
    transaction.onabort = () => { database.close(); reject(transaction.error ?? new Error('读取本地生成记录已中断')) }
  })
}

export async function deleteRedemptionHistory(id?: string): Promise<void> {
  const database = await openRedemptionHistory()
  return new Promise((resolve, reject) => {
    const transaction = database.transaction('generated_batches', 'readwrite')
    const store = transaction.objectStore('generated_batches')
    if (id == null) store.clear()
    else store.delete(id)
    transaction.oncomplete = () => { database.close(); resolve() }
    transaction.onerror = () => { database.close(); reject(transaction.error ?? new Error('删除本地生成记录失败')) }
    transaction.onabort = () => { database.close(); reject(transaction.error ?? new Error('删除本地生成记录已中断')) }
  })
}

export async function saveRedemptionHistory(record: RedemptionHistoryRecord): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.open('siteadmin-redemptions', 1)
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains('generated_batches')) {
        request.result.createObjectStore('generated_batches', { keyPath: 'id' })
      }
    }
    request.onerror = () => reject(request.error ?? new Error('无法打开本地生成记录'))
    request.onblocked = () => reject(new Error('本地生成记录正在被其他页面更新'))
    request.onsuccess = () => {
      const database = request.result
      const transaction = database.transaction('generated_batches', 'readwrite')
      const store = transaction.objectStore('generated_batches')
      store.put(record)
      const read = store.getAll()
      read.onsuccess = () => {
        const records = read.result as RedemptionHistoryRecord[]
        records.sort((a, b) => b.timestamp.localeCompare(a.timestamp))
        for (const old of records.slice(100)) store.delete(old.id)
      }
      transaction.oncomplete = () => { database.close(); resolve() }
      transaction.onerror = () => { database.close(); reject(transaction.error ?? new Error('保存本地生成记录失败')) }
      transaction.onabort = () => { database.close(); reject(transaction.error ?? new Error('本地生成记录保存已中断')) }
    }
  })
}
