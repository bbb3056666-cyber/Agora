export type UserSession = {
  token: string;
  refresh_token: string;
  account_id: number;
  username: string;
};

export type Author = { id: number; username: string };
export type Asset = { type: string; url: string; sort_order: number };

export type FeedItem = {
  id: number;
  author: Author;
  author_id?: number;
  username?: string;
  content_type: "video" | "image" | "article";
  title: string;
  description?: string;
  body?: string;
  play_url?: string;
  cover_url?: string;
  assets?: Asset[];
  create_time: number | string;
  likes_count: number;
  is_liked: boolean;
};

export type Comment = {
  id: number;
  author_id: number;
  username: string;
  content_id: number;
  content: string;
  created_at: string;
};

export type Profile = {
  account: { id: number; username: string; avatar_url?: string; bio?: string };
  content_count: number;
  total_likes: number;
  follower_count: number;
  vlogger_count: number;
};

export type Notification = {
  id: number;
  recipient_id: number;
  sender_id: number;
  type?: string;
  content?: string;
  is_read: boolean;
  created_at: string;
  [key: string]: unknown;
};
