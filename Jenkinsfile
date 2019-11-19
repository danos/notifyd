#!groovy

pipeline {
    agent any

    options {
        ansiColor('xterm')
        timestamps()
    }

    stages {
        stage('DRAM') {
            steps {
                sh "dram --username jenkins -d yang"
            }
        }
    }
}
