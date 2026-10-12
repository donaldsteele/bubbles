import type { User } from '../api/types';
import { onLocalReset } from '../reset';

class UserDirectory {
  byId = $state<Record<string, User>>({});

  load(users: User[]): void {
    const next: Record<string, User> = {};
    for (const u of users) next[u.id] = u;
    this.byId = next;
  }

  upsert(u: User): void {
    this.byId[u.id] = u;
  }

  get(id: string): User | undefined {
    return this.byId[id];
  }

  clear(): void {
    this.byId = {};
  }
}

export const users = new UserDirectory();

onLocalReset(() => users.clear());
