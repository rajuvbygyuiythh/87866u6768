export namespace bot {
	
	export class Status {
	    state: string;
	    matches: number;
	    replies: number;
	    line: number;
	    bots: number;
	    reals: number;
	    unknown: number;
	    last_event: string;
	    updated: string;
	    tag: string;
	    proxy?: string;
	    ua?: string;
	    port?: number;
	    snap_shares: number;
	    telegram_shares: number;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.matches = source["matches"];
	        this.replies = source["replies"];
	        this.line = source["line"];
	        this.bots = source["bots"];
	        this.reals = source["reals"];
	        this.unknown = source["unknown"];
	        this.last_event = source["last_event"];
	        this.updated = source["updated"];
	        this.tag = source["tag"];
	        this.proxy = source["proxy"];
	        this.ua = source["ua"];
	        this.port = source["port"];
	        this.snap_shares = source["snap_shares"];
	        this.telegram_shares = source["telegram_shares"];
	    }
	}

}

export namespace dash {
	
	export class SessionStats {
	    chats: number;
	    replies: number;
	    snap_shares: number;
	    telegram_shares: number;
	
	    static createFrom(source: any = {}) {
	        return new SessionStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chats = source["chats"];
	        this.replies = source["replies"];
	        this.snap_shares = source["snap_shares"];
	        this.telegram_shares = source["telegram_shares"];
	    }
	}
	export class registerStatus {
	    running: boolean;
	    message: string;
	    done: boolean;
	
	    static createFrom(source: any = {}) {
	        return new registerStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.message = source["message"];
	        this.done = source["done"];
	    }
	}
	export class Snapshot {
	    settings: Record<string, string>;
	    accounts: store.Account[];
	    bot_status: Record<string, bot.Status>;
	    register: registerStatus;
	    bots_running: boolean;
	    bots_message: string;
	    logs: string[];
	    stats: SessionStats;
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = source["settings"];
	        this.accounts = this.convertValues(source["accounts"], store.Account);
	        this.bot_status = this.convertValues(source["bot_status"], bot.Status, true);
	        this.register = this.convertValues(source["register"], registerStatus);
	        this.bots_running = source["bots_running"];
	        this.bots_message = source["bots_message"];
	        this.logs = source["logs"];
	        this.stats = this.convertValues(source["stats"], SessionStats);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace reply {
	
	export class Settings {
	    chat_mode: string;
	    share_platform: string;
	    folder_path: string;
	    ai_base_url: string;
	    ai_api_key: string;
	    ai_model: string;
	    api_temperature: number;
	    api_share_turn: number;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat_mode = source["chat_mode"];
	        this.share_platform = source["share_platform"];
	        this.folder_path = source["folder_path"];
	        this.ai_base_url = source["ai_base_url"];
	        this.ai_api_key = source["ai_api_key"];
	        this.ai_model = source["ai_model"];
	        this.api_temperature = source["api_temperature"];
	        this.api_share_turn = source["api_share_turn"];
	    }
	}

}

export namespace store {
	
	export class Account {
	    user_id: string;
	    email: string;
	    has_session: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Account(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.user_id = source["user_id"];
	        this.email = source["email"];
	        this.has_session = source["has_session"];
	    }
	}

}

