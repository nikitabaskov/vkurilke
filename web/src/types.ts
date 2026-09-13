export interface User { id: number; first_name: string; username: string; photo_url: string; bot_started: boolean }
export interface Preferences { session: boolean; arrival: boolean; departure: boolean }
export interface Me { user: User; preferences: Preferences; is_admin: boolean }
export interface Group { id: string; name: string; role: 'owner' | 'admin' | 'member'; invite_code?: string }
export interface Member extends User { role: Group['role']; status: 'idle' | 'going' | 'smoking'; started_at: number; target_time: number }
export interface Active { group_id: string; status: 'going' | 'smoking'; started_at: number; target_time: number; check_token?: string; check_deadline: number }
export interface Snapshot { members: Member[]; active: Active | null; server_time: number }
export interface Meta { bot_username: string; check_minutes: number; answer_minutes: number }
export type StatisticsPeriod = 'today' | 'week' | 'month' | 'all';
export interface SmokingTotals { outings: number; seconds: number }
export interface PersonStatistics extends SmokingTotals { user_id: number; name: string }
export interface Statistics { period: StatisticsPeriod; from: number; server_time: number; timezone: string; totals: SmokingTotals; sessions: number; session_seconds: number; leaderboard: PersonStatistics[] }
interface WebApp {
  initData: string;
  colorScheme: 'dark' | 'light';
  themeParams: { hint_color?: string };
  isActive?: boolean;
  ready(): void;
  expand(): void;
  onEvent(name: string, fn: () => void): void;
  setHeaderColor(color: string): void;
  setBackgroundColor(color: string): void;
  openTelegramLink(url: string): void;
  HapticFeedback?: { impactOccurred(style: 'light' | 'medium'): void; notificationOccurred(type: 'success' | 'error'): void };
}
declare global { interface Window { Telegram?: { WebApp: WebApp } } }
