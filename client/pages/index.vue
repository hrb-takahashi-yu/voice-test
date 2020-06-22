<template>
    <div class="container">
        <div>
            <!-- <div class="result">{{res+tmp}}</div> -->

            <!-- <div class="links">
                <span>username：</span>
                <input type="text" name="username" size="30" v-model="username" />

                <span>password：</span>
                <input type="password" name="password" size="30" v-model="password" />
            </div>-->

            <div class="links">
                <span>room ID：</span>
                <input type="text" name="id" size="30" v-model="roomId" />

                <span>name：</span>
                <input type="text" name="name" size="30" v-model="name" />
            </div>

            <div class="links">
                <a @click="createRoom" class="button--green">create room</a>
            </div>

            <div class="links">
                <a @click="start" class="button--green" :class="on?'on':''">start</a>
                <a @click="stop" class="button--grey" :class="on?'':'off'">stop</a>
            </div>

            <div class="textarea">
                <template v-for="(talk,index) in conversation">
                    <template v-if="talk.text!==''">
                        <div :key="index">{{talk.name}} : {{talk.text}}</div>
                    </template>
                </template>
            </div>
        </div>
    </div>
</template>

<script lang="ts">
import axios from "axios";
import { Component, Vue } from "vue-property-decorator";
import Route from "vue-router/types/vue";

type Room = {
    roomId: string;
};

@Component
export default class Index extends Vue {
    connection?: WebSocket;
    streams: MediaStream[] = [];

    on = false;

    // res = "";

    // tmp = "";

    roomId = "";
    name = "";
    username = "";
    password = "";

    conversation: { name: string; text: string; createdAt: string }[] = [];

    get url(): string {
        let scheme = window.location.protocol == "https:" ? "wss://" : "ws://";
        let webSocketUri =
            scheme +
            window.location.hostname +
            // ":3004" +
            (location.port ? ":" + location.port : "") +
            `/speaker?id=${this.roomId}&name=${this.name}`;
        // `/speaker?id=${this.roomId}&name=${this.name}&username=${this.username}&password=${this.password}`;

        return webSocketUri;
        // return `ws://localhost:${port}/speaker?id=${this.roomId}&name=${this.name}&username=${this.username}&password=${this.password}`;
    }

    created() {
        if (typeof this.$route.query.id === "string") {
            this.roomId = this.$route.query.id;
        }
    }
    // created() {
    //     // this.connection = new WebSocket("ws://localhost:3005/speaker");
    //     this.connection = new WebSocket(this.url);
    //     // this.connection = new WebSocket("ws://192.168.3.19:3004/speaker");

    //     this.connection.onmessage = async event => {
    //         const text = await event.data.text();
    //         console.log(text);
    //         this.tmp = text;
    //         this.conversation = JSON.parse(text);
    //         console.log(this.conversation);
    //     };
    // }

    start() {
        this.on = true;
        if (this.connection) {
            return;
        }
        const handleSuccess = (stream: MediaStream) => {
            this.streams.push(stream);
            const context = new AudioContext();
            const input = context.createMediaStreamSource(stream);
            const processor = context.createScriptProcessor(4096, 1, 1);

            this.connection = new WebSocket(this.url);
            // this.connection = new WebSocket("ws://192.168.3.19:3004/speaker");

            this.connection.onmessage = async event => {
                const text = await event.data.text();
                // console.log(text);
                // this.tmp = text;
                try {
                    // TODO: エラーハンドリングが雑
                    this.conversation = JSON.parse(text);
                } catch {
                    processor.onaudioprocess = null;
                    this.stop();
                    alert(text);
                }
                console.log(this.conversation);
            };

            this.connection.onclose = event => {
                console.log(event);
                processor.onaudioprocess = null;
                this.stop();
                return;
            };

            input.connect(processor);
            processor.connect(context.destination);

            this.connection.onopen = () => {
                console.log("opened");
                processor.onaudioprocess = e => {
                    if (!this.connection) {
                        processor.onaudioprocess = null;
                        return;
                    }

                    const voice = e.inputBuffer.getChannelData(0);

                    // TODO: サンプルレートをどうするか
                    // フロントでdownRateする or サーバに送信して指定する
                    // console.log("sample rate:" + e.inputBuffer.sampleRate);

                    let buf = new ArrayBuffer(voice.length * 2);
                    let dv = new DataView(buf);

                    this.floatTo16BitPCM(dv, voice);
                    this.connection.send(buf);
                };
            };
        };

        navigator.mediaDevices
            .getUserMedia({ audio: true, video: false })
            // .getDisplayMedia({ audio: true, video: true })
            .then(handleSuccess);
    }

    floatTo16BitPCM(output: DataView, input: Float32Array) {
        let offset = 0;
        for (let i = 0; i < input.length; i++, offset += 2) {
            let s = Math.max(-1, Math.min(1, input[i]));
            output.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true);
        }
    }

    downRate(
        buffer: Float32Array,
        fromRate: number,
        toRate: number
    ): Float32Array {
        const rate = fromRate / toRate;
        const result = new Float32Array(Math.round(buffer.length / rate));
        let offsetResult = 0;
        let offsetBuffer = 0;
        while (offsetResult < result.length) {
            let nextOffsetBuffer = Math.round((offsetResult + 1) * rate);
            let accum = 0;
            let count = 0;
            for (
                var i = offsetBuffer;
                i < nextOffsetBuffer && i < buffer.length;
                i++
            ) {
                accum += buffer[i];
                count++;
            }
            result[offsetResult] = accum / count;
            offsetResult++;
            offsetBuffer = nextOffsetBuffer;
        }
        return result;
    }

    stop() {
        this.on = false;
        if (!this.connection) {
            return;
        }
        // this.res = this.res + this.tmp;
        // this.tmp = "";
        this.connection.close();
        this.connection = undefined;
        this.streams.forEach(stream =>
            stream.getTracks().forEach(track => track.stop())
        );
        this.streams = [];
    }

    async createRoom() {
        let room: Room = { roomId: "" };
        try {
            const { data } = await axios.post("/room");
            // const { data } = await axios.post(
            //     `/room?username=${this.username}&password=${this.password}`
            // );
            room = data;
        } catch (error) {
            alert(error);
            return;
        }
        alert(
            `${window.location.protocol}//${window.location.hostname}${
                location.port ? ":" + location.port : ""
            }?id=${room.roomId}`
        );
        this.roomId = room.roomId;
    }
}
</script>

<style>
.container {
    margin: 30px auto 0;
    min-height: 100vh;
    display: flex;
    justify-content: center;
    /* align-items: center; */
    text-align: center;
}

.title {
    font-family: "Quicksand", "Source Sans Pro", -apple-system,
        BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial,
        sans-serif;
    display: block;
    font-weight: 300;
    font-size: 100px;
    color: #35495e;
    letter-spacing: 1px;
}

.subtitle {
    font-weight: 300;
    font-size: 42px;
    color: #526488;
    word-spacing: 5px;
    padding-bottom: 15px;
}

.links {
    padding-top: 15px;
}

.on {
    color: #fff;
    background-color: #3b8070;
}

.off {
    color: #fff;
    background-color: #35495e;
}
.result {
    white-space: pre-wrap;
}

.textarea {
    padding: 0 20px;
    text-align: left;
}
</style>
